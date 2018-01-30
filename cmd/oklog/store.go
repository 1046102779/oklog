package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/cors"

	"github.com/1046102779/oklog/pkg/cluster"
	"github.com/1046102779/oklog/pkg/fs"
	"github.com/1046102779/oklog/pkg/group"
	"github.com/1046102779/oklog/pkg/store"
	"github.com/1046102779/oklog/pkg/ui"
)

//                                    +-1---------+ +-1----+
//                                    | store.Log | | Peer |
//                                    +-----------+ +------+
// +-1------------+   +-2---------+      ^ ^ ^        ^ ^
// | API listener |<--| Store API |------' | |        | |
// |              |   |           |-------------------' |
// +--------------+   +-----------+        | |          |
//                    +-2---------+        | |          |
//                    | Compacter |--------' |          |
//                    +-----------+          |          |
//                    +-2---------+          |          |
//                    | Consumer  |----------'          |
//                    |           |---------------------'
//                    +-----------+
const (
	defaultStoreSegmentConsumers         = 1
	defaultStoreSegmentTargetSize        = 128 * 1024 * 1024
	defaultStoreSegmentTargetAge         = 3 * time.Second
	defaultStoreSegmentBufferSize        = 1024 * 1024
	defaultStoreSegmentReplicationFactor = 2
	defaultStoreSegmentRetain            = 7 * 24 * time.Hour
	defaultStoreSegmentPurge             = 24 * time.Hour
)

var (
	defaultStorePath = filepath.Join("data", "store")
)

type StoreConfig struct {
	Debug                    *bool          `json:"debug"`
	MonitorApiAddr           *string        `json:"api_addr"`
	ClusterBindAddr          *string        `json:"cluster_bind_addr"`
	StorePath                *string        `json:"store_path"`
	SegmentConsumers         *int           `json:"segment_consumers"`
	SegmentTargetSize        *int64         `json:"segment_target_size"`
	SegmentTargetAge         *time.Duration `json:"segment_target_age"`
	SegmentBufferSize        *int64         `json:"segment_buffer_size"`
	SegmentReplicationFactor *int           `json:"segment_replication_tactor"`
	SegmentRetain            *time.Duration `json:"segment_retain"`
	SegmentPurge             *time.Duration `json:"segment_purge"`
	UiLocal                  *bool          `json:"segment_purge"`
	ClusterPeers             stringslice    `json:"cluster_peers"`
}

func parseStoreInputParams(args []string) (config *StoreConfig, err error) {
	flagset := flag.NewFlagSet("store", flag.ExitOnError)
	config = &StoreConfig{
		Debug:                    flagset.Bool("debug", false, "debug logging"),
		MonitorApiAddr:           flagset.String("api", defaultAPIAddr, "listen address for store API"),
		ClusterBindAddr:          flagset.String("cluster", defaultClusterAddr, "listen address for cluster"),
		StorePath:                flagset.String("store.path", defaultStorePath, "path holding segment files for storage tier"),
		SegmentConsumers:         flagset.Int("store.segment-consumers", defaultStoreSegmentConsumers, "concurrent segment consumers"),
		SegmentTargetSize:        flagset.Int64("store.segment-target-size", defaultStoreSegmentTargetSize, "try to keep store segments about this size"),
		SegmentTargetAge:         flagset.Duration("store.segment-target-age", defaultStoreSegmentTargetAge, "replicate once the aggregate segment is this old"),
		SegmentBufferSize:        flagset.Int64("store.segment-buffer-size", defaultStoreSegmentBufferSize, "per-segment in-memory read buffer during queries"),
		SegmentReplicationFactor: flagset.Int("store.segment-replication-factor", defaultStoreSegmentReplicationFactor, "how many copies of each segment to replicate"),
		SegmentRetain:            flagset.Duration("store.segment-retain", defaultStoreSegmentRetain, "retention period for segment files"),
		SegmentPurge:             flagset.Duration("store.segment-purge", defaultStoreSegmentPurge, "purge deleted segment files after this long"),
		UiLocal:                  flagset.Bool("ui.local", false, "ignore embedded files and go straight to the filesystem"),
	}
	flagset.Var(&config.ClusterPeers, "peer", "cluster peer host:port (repeatable)")
	flagset.Usage = usageFor(flagset, "oklog store [flags]")
	if err = flagset.Parse(args); err != nil {
		return
	}
	return
}

type StoreMetrics struct {
	ApiDuration        *prometheus.HistogramVec
	CompactDuration    *prometheus.HistogramVec
	ConsumedSegments   prometheus.Counter
	ConsumedBytes      prometheus.Counter
	ReplicatedSegments *prometheus.CounterVec
	ReplicatedBytes    *prometheus.CounterVec
	TrashedSegments    *prometheus.CounterVec
	PurgedSegments     *prometheus.CounterVec
}

func registerStoreMetrics() (metrics *StoreMetrics) {
	metrics = new(StoreMetrics)
	// Instrumentation.
	metrics.ApiDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "oklog",
		Name:      "api_request_duration_seconds",
		Help:      "API request duration in seconds.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "path", "status_code"})
	metrics.CompactDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "oklog",
		Name:      "store_compact_duration_seconds",
		Help:      "Duration of each compaction in seconds.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"kind", "compacted", "result"})
	metrics.ConsumedSegments = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "oklog",
		Name:      "store_consumed_segments",
		Help:      "Segments consumed from ingest nodes.",
	})
	metrics.ConsumedBytes = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "oklog",
		Name:      "store_consumed_bytes",
		Help:      "Bytes consumed from ingest nodes.",
	})
	metrics.ReplicatedSegments = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "oklog",
		Name:      "store_replicated_segments",
		Help:      "Segments replicated, by direction i.e. ingress or egress.",
	}, []string{"direction"})
	metrics.ReplicatedBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "oklog",
		Name:      "store_replicated_bytes",
		Help:      "Segments replicated, by direction i.e. ingress or egress.",
	}, []string{"direction"})
	metrics.TrashedSegments = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "oklog",
		Name:      "store_trashed_segments",
		Help:      "Segments moved to trash.",
	}, []string{"success"})
	metrics.PurgedSegments = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "oklog",
		Name:      "store_purged_segments",
		Help:      "Segments purged from trash.",
	}, []string{"success"})
	prometheus.MustRegister(
		metrics.ApiDuration,
		metrics.CompactDuration,
		metrics.ConsumedSegments,
		metrics.ConsumedBytes,
		metrics.ReplicatedSegments,
		metrics.ReplicatedBytes,
		metrics.TrashedSegments,
		metrics.PurgedSegments,
	)
	return
}

func runStore(args []string) (err error) {
	var (
		metrics *StoreMetrics
		config  *StoreConfig
	)
	if config, err = parseStoreInputParams(args); err != nil {
		return
	}
	// Logging.
	var logger log.Logger
	{
		logLevel := level.AllowInfo()
		if *config.Debug {
			logLevel = level.AllowAll()
		}
		logger = log.NewLogfmtLogger(os.Stderr)
		logger = log.With(logger, "ts", log.DefaultTimestampUTC)
		logger = level.NewFilter(logger, logLevel)
	}

	metrics = registerStoreMetrics()
	// Parse URLs for listeners.
	apiNetwork, apiAddress, _, apiPort, err := parseAddr(*config.MonitorApiAddr, defaultAPIPort)
	if err != nil {
		return err
	}
	_, _, clusterBindHost, clusterBindPort, err := parseAddr(*config.ClusterBindAddr, defaultClusterPort)
	if err != nil {
		return err
	}
	level.Info(logger).Log("cluster_bind", fmt.Sprintf("%s:%d", clusterBindHost, clusterBindPort))

	// Bind listeners.
	apiListener, err := net.Listen(apiNetwork, apiAddress)
	if err != nil {
		return err
	}
	level.Info(logger).Log("API", fmt.Sprintf("%s://%s", apiNetwork, apiAddress))

	// Create storelog.
	storeLog, err := store.NewFileLog(
		fs.NewRealFilesystem(),
		*config.StorePath,
		*config.SegmentTargetSize, *config.SegmentBufferSize,
		store.LogReporter{Logger: log.With(logger, "component", "FileLog")},
	)
	if err != nil {
		return err
	}
	defer func() {
		if err := storeLog.Close(); err != nil {
			level.Error(logger).Log("err", err)
		}
	}()
	level.Info(logger).Log("StoreLog", *config.StorePath)

	// Create peer.
	peer, err := cluster.NewPeer(
		clusterBindHost, clusterBindPort,
		clusterBindHost, clusterBindPort, // instead of clusterAdvertiserHost&Port
		config.ClusterPeers,
		cluster.PeerTypeStore, apiPort,
		log.With(logger, "component", "cluster"),
	)
	if err != nil {
		return err
	}
	prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "oklog",
		Name:      "cluster_size",
		Help:      "Number of peers in the cluster from this node's perspective.",
	}, func() float64 { return float64(peer.ClusterSize()) }))

	return startStoreGroup(peer, storeLog, config, metrics, apiListener, logger)
}

func startStoreGroup(peer *cluster.Peer,
	storeLog store.Log,
	config *StoreConfig, metrics *StoreMetrics,
	apiListener net.Listener,
	logger log.Logger,
) (err error) {
	// Create the HTTP clients we'll use for various purposes.
	unlimitedClient := http.DefaultClient // no timeouts, be careful
	timeoutClient := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 5 * time.Second,
			Dial: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).Dial,
			TLSHandshakeTimeout: 10 * time.Second,
			DisableKeepAlives:   false,
			MaxIdleConnsPerHost: 1,
		},
	}
	// Execution group.
	var g group.Group
	{
		cancel := make(chan struct{})
		g.Add(func() error {
			<-cancel
			return peer.Leave(time.Second)
		}, func(error) {
			close(cancel)
		})
	}
	for i := 0; i < *config.SegmentConsumers; i++ {
		c := store.NewConsumer(
			peer,
			timeoutClient,
			*config.SegmentTargetSize,
			*config.SegmentTargetAge,
			*config.SegmentReplicationFactor,
			metrics.ConsumedSegments,
			metrics.ConsumedBytes,
			metrics.ReplicatedSegments.WithLabelValues("egress"),
			metrics.ReplicatedBytes.WithLabelValues("egress"),
			store.LogReporter{Logger: log.With(logger, "component", "Consumer")},
		)
		g.Add(func() error {
			c.Run()
			return nil
		}, func(error) {
			c.Stop()
		})
	}
	{
		c := store.NewCompacter(
			storeLog,
			*config.SegmentTargetSize,
			*config.SegmentRetain,
			*config.SegmentPurge,
			metrics.CompactDuration,
			metrics.TrashedSegments,
			metrics.PurgedSegments,
			store.LogReporter{Logger: log.With(logger, "component", "Compacter")},
		)
		g.Add(func() error {
			c.Run()
			return nil
		}, func(error) {
			c.Stop()
		})
	}
	{
		g.Add(func() error {
			mux := http.NewServeMux()
			api := store.NewAPI(
				peer,
				storeLog,
				timeoutClient,
				unlimitedClient,
				metrics.ReplicatedSegments.WithLabelValues("ingress"),
				metrics.ReplicatedBytes.WithLabelValues("ingress"),
				metrics.ApiDuration,
				store.LogReporter{Logger: log.With(logger, "component", "API")},
			)
			defer func() {
				if err := api.Close(); err != nil {
					level.Warn(logger).Log("err", err)
				}
			}()
			mux.Handle("/store/", http.StripPrefix("/store", api))
			mux.Handle("/ui/", ui.NewAPI(logger, *config.UiLocal))
			registerMetrics(mux)
			registerProfile(mux)
			return http.Serve(apiListener, cors.Default().Handler(mux))
		}, func(error) {
			apiListener.Close()
		})
	}
	{
		cancel := make(chan struct{})
		g.Add(func() error {
			return interrupt(cancel)
		}, func(error) {
			close(cancel)
		})
	}
	return g.Run()
}
