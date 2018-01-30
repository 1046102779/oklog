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
	"github.com/1046102779/oklog/pkg/ingest"
)

const (
	defaultFastPort                    = 7651
	defaultIngestSegmentFlushSize      = 16 * 1024 * 1024
	defaultIngestSegmentFlushAge       = 3 * time.Second
	defaultIngestSegmentPendingTimeout = time.Minute
)

var (
	defaultFastAddr   = fmt.Sprintf("tcp://0.0.0.0:%d", defaultFastPort)
	defaultIngestPath = filepath.Join("data", "ingest")
)

type IngestConfig struct {
	Debug                 *bool          `json:"debug"`
	MonitorApiAddr        *string        `json:"api_addr"`
	FastAddr              *string        `json:"fast_addr"`
	ClusterBindAddr       *string        `json:"cluster_bind_addr"`
	ClusterAdvertiseAddr  *string        `json:"cluster_advertise_addr"`
	IngestPath            *string        `json:"ingest_path"`
	SegmentFlushSize      *int           `json:"segment_flush_size"`
	SegmentFlushAge       *time.Duration `json:"segment_flush_age"`
	SegmentPendingTimeout *time.Duration `json:"segment_pending_timeout"`
	ClusterPeers          stringslice    `json:"cluster_peers"`
}

func parseIngestParams(args []string) (config *IngestConfig, err error) {
	flagset := flag.NewFlagSet("ingest", flag.ExitOnError)
	config = &IngestConfig{
		Debug:                 flagset.Bool("debug", false, "debug logging"),
		MonitorApiAddr:        flagset.String("api", defaultAPIAddr, "listen address for ingest API"),
		FastAddr:              flagset.String("ingest.fast", defaultFastAddr, "listen address for fast (async) writes"),
		ClusterBindAddr:       flagset.String("cluster", defaultClusterAddr, "listen address for cluster"),
		IngestPath:            flagset.String("ingest.path", defaultIngestPath, "path holding segment files for ingest tier"),
		SegmentFlushSize:      flagset.Int("ingest.segment-flush-size", defaultIngestSegmentFlushSize, "flush segments after they grow to this size"),
		SegmentFlushAge:       flagset.Duration("ingest.segment-flush-age", defaultIngestSegmentFlushAge, "flush segments after they are active for this long"),
		SegmentPendingTimeout: flagset.Duration("ingest.segment-pending-timeout", defaultIngestSegmentPendingTimeout, "claimed but uncommitted pending segments are failed after this long"),
	}
	flagset.Var(&config.ClusterPeers, "peer", "cluster peer host:port (repeatable)")
	flagset.Usage = usageFor(flagset, "oklog ingest [flags]")
	if err = flagset.Parse(args); err != nil {
		return
	}
	return
}

type IngestMetrics struct {
	ConnectedClients      *prometheus.GaugeVec
	IngestWriterBytes     prometheus.Counter
	IngestWriterRecords   prometheus.Counter
	IngestWriterSyncs     prometheus.Counter
	IngestWriterRotations *prometheus.CounterVec
	FlushedSegmentAge     prometheus.Histogram
	FlushedSegmentSize    prometheus.Histogram
	FailedSegments        prometheus.Counter
	CommittedSegments     prometheus.Counter
	CommittedBytes        prometheus.Counter
	ApiDuration           *prometheus.HistogramVec
}

func registerIngestMetrics() (metrics *IngestMetrics) {
	// Instrumentation.
	metrics = new(IngestMetrics)
	metrics.ConnectedClients = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "oklog",
		Name:      "connected_clients",
		Help:      "Number of currently connected clients by modality.",
	}, []string{"modality"})
	metrics.IngestWriterBytes = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "oklog",
		Name:      "ingest_writer_bytes_written_total",
		Help:      "The total number of bytes written.",
	})
	metrics.IngestWriterRecords = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "oklog",
		Name:      "ingest_writer_records_written_total",
		Help:      "The total number of records written.",
	})
	metrics.IngestWriterSyncs = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "oklog",
		Name:      "ingest_writer_syncs_total",
		Help:      "The number of times an active segment is explicitly fsynced.",
	})
	metrics.IngestWriterRotations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "oklog",
		Name:      "ingest_writer_flushes_total",
		Help:      "The number of times an active segment is flushed.",
	}, []string{"reason"})
	metrics.FlushedSegmentAge = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "oklog",
		Name:      "ingest_segment_flush_age_seconds",
		Help:      "Age of segment when flushed in seconds.",
		Buckets:   prometheus.DefBuckets,
	})
	metrics.FlushedSegmentSize = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "oklog",
		Name:      "ingest_segment_flush_size_bytes",
		Help:      "Size of active segment when flushed in bytes.",
		Buckets:   []float64{1 << 14, 1 << 15, 1 << 16, 1 << 17, 1 << 18, 1 << 19, 1 << 20, 1 << 21, 1 << 22, 1 << 23, 1 << 24},
	})
	metrics.FailedSegments = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "oklog",
		Name:      "ingest_failed_segments",
		Help:      "Segments consumed, but failed and returned to flushed.",
	})
	metrics.CommittedSegments = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "oklog",
		Name:      "ingest_committed_segments",
		Help:      "Segments successfully consumed and committed.",
	})
	metrics.CommittedBytes = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "oklog",
		Name:      "ingest_committed_bytes",
		Help:      "Bytes successfully consumed and committed.",
	})
	metrics.ApiDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "oklog",
		Name:      "api_request_duration_seconds",
		Help:      "API request duration in seconds.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "path", "status_code"})
	prometheus.MustRegister(
		metrics.ConnectedClients,
		metrics.IngestWriterBytes,
		metrics.IngestWriterRecords,
		metrics.IngestWriterSyncs,
		metrics.IngestWriterRotations,
		metrics.FlushedSegmentAge,
		metrics.FlushedSegmentSize,
		metrics.FailedSegments,
		metrics.CommittedSegments,
		metrics.CommittedBytes,
		metrics.ApiDuration,
	)
	return
}

func runIngest(args []string) (err error) {
	var (
		config  *IngestConfig
		metrics *IngestMetrics
	)
	if config, err = parseIngestParams(args); err != nil {
		return
	}

	// +-1----------------+   +-2----------+   +-1----------+ +-1----+
	// | Fast listener    |<--| Write      |-->| ingest.Log | | Peer |
	// +------------------+   | handler    |   +------------+ +------+
	// +-1----------------+   |            |     ^              ^
	// | Durable listener |<--|            |     |              |
	// +------------------+   |            |     |              |
	// +-1----------------+   |            |     |              |
	// | Bulk listener    |<--|            |     |              |
	// +------------------+   +------------+     |              |
	// +-1----------------+   +-2----------+     |              |
	// | API listener     |<--| Ingest API |-----'              |
	// |                  |   |            |--------------------'
	// +------------------+   +------------+

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

	metrics = registerIngestMetrics()
	// Parse listener addresses.
	fastListener, apiListener,
		apiPort,
		ingestLog,
		err := parseListeners(config, logger)
	if err != nil {
		return err
	}
	_, _, clusterBindHost, clusterBindPort, err := parseAddr(*config.ClusterBindAddr, defaultClusterPort)
	if err != nil {
		return err
	}
	level.Info(logger).Log("cluster_bind", fmt.Sprintf("%s:%d", clusterBindHost, clusterBindPort))
	// Create peer.
	var peer *cluster.Peer
	if peer, err = cluster.NewPeer(
		clusterBindHost, clusterBindPort,
		clusterBindHost, clusterBindPort, // instead of clusterAdvertiseHost, clusterAdvertisePort,
		config.ClusterPeers,
		cluster.PeerTypeIngest, apiPort,
		log.With(logger, "component", "cluster"),
	); err != nil {
		return err
	}
	prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "oklog",
		Name:      "cluster_size",
		Help:      "Number of peers in the cluster from this node's perspective.",
	}, func() float64 { return float64(peer.ClusterSize()) }))

	// Execution group.
	return startIngestGroup(peer, ingestLog, config, metrics, fastListener, apiListener)
}

func parseListeners(config *IngestConfig, logger log.Logger) (
	fastListener, apiListener net.Listener,
	apiPort int,
	ingestLog ingest.Log,
	err error) {
	var (
		fastNetwork, fastAddress string
		apiNetwork, apiAddress   string
	)
	if fastNetwork, fastAddress, _, _, err = parseAddr(*config.FastAddr, defaultFastPort); err != nil {
		return
	}
	if apiNetwork, apiAddress, _, apiPort, err = parseAddr(*config.MonitorApiAddr, defaultAPIPort); err != nil {
		return
	}

	// Bind listeners.
	if fastListener, err = net.Listen(fastNetwork, fastAddress); err != nil {
		return
	}
	level.Info(logger).Log("fast", fmt.Sprintf("%s://%s", fastNetwork, fastAddress))
	if apiListener, err = net.Listen(apiNetwork, apiAddress); err != nil {
		return
	}
	level.Info(logger).Log("API", fmt.Sprintf("%s://%s", apiNetwork, apiAddress))

	// Create ingest log.
	if ingestLog, err = ingest.NewFileLog(fs.NewRealFilesystem(), *config.IngestPath); err != nil {
		return
	}
	defer func() {
		if err = ingestLog.Close(); err != nil {
			level.Error(logger).Log("err", err)
		}
	}()
	level.Info(logger).Log("ingest_path", *config.IngestPath)
	return
}

// manage goroutine lifecycle
func startIngestGroup(peer *cluster.Peer,
	ingestLog ingest.Log,
	config *IngestConfig, metrics *IngestMetrics,
	fastListener, apiListener net.Listener,
) (err error) {
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
	{
		g.Add(func() error {
			return ingest.HandleConnections(
				fastListener,
				ingest.HandleFastWriter,
				ingestLog,
				*config.SegmentFlushAge, *config.SegmentFlushSize,
				metrics.ConnectedClients.WithLabelValues("fast"),
				metrics.IngestWriterBytes, metrics.IngestWriterRecords, metrics.IngestWriterSyncs,
				metrics.FlushedSegmentAge, metrics.FlushedSegmentSize,
			)
		}, func(error) {
			fastListener.Close()
		})
		g.Add(func() error {
			mux := http.NewServeMux()
			mux.Handle("/ingest/", http.StripPrefix("/ingest", ingest.NewAPI(
				peer,
				ingestLog,
				*config.SegmentPendingTimeout,
				metrics.FailedSegments,
				metrics.CommittedSegments,
				metrics.CommittedBytes,
				metrics.ApiDuration,
			)))
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
