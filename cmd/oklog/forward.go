package main

import (
	"flag"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/streadway/amqp"
)

type ForwardConfig struct {
	Debug          *bool       `json:"debug"`
	MonitorApiAddr *string     `json:"api_addr"`
	MQConfig       *string     `json:"mqpath"`
	Prefixes       stringslice `json:"prefixes"`
	ExternalArgs   []string    `json:"external_args"`
}

type ForwardMetrics struct {
	Bytes           prometheus.Counter // forward received total bytes
	Records         prometheus.Counter // forward received the number of  records
	Disconnects     prometheus.Counter // forward received disconnects times
	ShortWriteBytes prometheus.Counter // forward write bytes to ingest less than actual bytes
}

func registerForwardMetrics(config *ForwardConfig) (metrics *ForwardMetrics, err error) {
	metrics = new(ForwardMetrics)
	// Instrumentation.
	metrics.Bytes = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "oklog",
		Name:      "forward_bytes_total",
		Help:      "Bytes forwarded.",
	})
	metrics.Records = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "oklog",
		Name:      "forward_records_total",
		Help:      "Records forwarded.",
	})
	metrics.Disconnects = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "oklog",
		Name:      "forward_disconnects",
		Help:      "Number of times forwarder is disconnected from ingester.",
	})
	metrics.ShortWriteBytes = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "oklog",
		Name:      "forward_short_writes",
		Help:      "Number of times forwarder performs a short write to the ingester.",
	})
	prometheus.MustRegister(
		metrics.Bytes,
		metrics.Disconnects,
		metrics.Records,
		metrics.ShortWriteBytes,
	)
	// For now, just a quick-and-dirty metrics server.
	if *config.MonitorApiAddr != "" {
		var (
			apiNetwork, apiAddress string
			apiListener            net.Listener
		)
		apiNetwork, apiAddress, _, _, err = parseAddr(*config.MonitorApiAddr, defaultAPIPort)
		if err != nil {
			return
		}
		apiListener, err = net.Listen(apiNetwork, apiAddress)
		if err != nil {
			return
		}
		go func() {
			mux := http.NewServeMux()
			registerMetrics(mux)
			registerProfile(mux)
			panic(http.Serve(apiListener, mux))
		}()
	}
	return
}

func parseInputParams(args []string) (config *ForwardConfig, err error) {
	config = new(ForwardConfig)
	flagset := flag.NewFlagSet("forward", flag.ExitOnError)
	{
		config.Debug = flagset.Bool("debug", false, "debug logging")
		config.MonitorApiAddr = flagset.String("api", "", "listen address for forward API (and metrics)")
		config.MQConfig = flagset.String("mq", "", "connect rabbit server config, such as: amqp://name:passwd@127.0.0.1:5672/domain?heartbeat=15")
		flagset.Var(&config.Prefixes, "prefix", "prefix annotated on each log record (repeatable)")
		flagset.Usage = usageFor(flagset, "oklog forward [flags] <ingester> [<ingester>...]")
	}
	if err = flagset.Parse(args); err != nil {
		return
	}
	config.ExternalArgs = flagset.Args()
	if len(config.ExternalArgs) <= 0 {
		errors.New("specify at least one ingest address as an argument")
		return
	}
	return
}

func parseForwardExternalArgs(args []string) (urls []*url.URL, err error) {
	var schema, host string
	// Parse URLs for forwarders.
	for _, addr := range args {
		if schema, host, _, _, err = parseAddr(addr, defaultFastPort); err != nil {
			err = errors.Wrap(err, "parsing ingest address")
			return
		}
		var u *url.URL
		if u, err = url.Parse(fmt.Sprintf("%s://%s", schema, host)); err != nil {
			err = errors.Wrap(err, "parsing ingest URL")
			return
		}
		if _, _, err = net.SplitHostPort(u.Host); err != nil {
			err = errors.Wrapf(err, "couldn't split host:port")
			return
		}
		urls = append(urls, u)
	}
	return
}

func runForward(args []string) (err error) {
	var (
		config  *ForwardConfig // input params
		metrics *ForwardMetrics
		urls    []*url.URL
	)
	// parse command line params
	if config, err = parseInputParams(args); err != nil {
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

	// register & start prometheus metrics instance
	if metrics, err = registerForwardMetrics(config); err != nil {
		return
	}

	// parse ingest nodes
	if urls, err = parseForwardExternalArgs(config.ExternalArgs); err != nil {
		return
	}

	// Construct the prefix expression.
	var prefix string
	if len(config.Prefixes) > 0 {
		prefix = strings.Join(config.Prefixes, " ") + " "
	}

	// Shuffle the order.
	rand.Seed(time.Now().UnixNano())
	for i := range urls {
		j := rand.Intn(i + 1)
		urls[i], urls[j] = urls[j], urls[i]
	}

	// Build a scanner for the input, and the last record we scanned.
	// These both outlive any individual connection to an ingester.
	// TODO(pb): have flag for backpressure vs. drop
	var (
		backoff = time.Duration(0)
	)

	// Enter the connect and forward loop. We do this forever.
	for ; ; urls = append(urls[1:], urls[0]) { // rotate thru URLs
		// We gonna try to connect to this first one.
		target := urls[0]

		// for now , only support tcp
		conn, err := net.Dial(target.Scheme, target.Host)
		if err != nil {
			level.Warn(logger).Log("Dial", target.String(), "err", err)
			backoff = exponential(backoff)
			time.Sleep(backoff)
			continue
		}
		defer conn.Close()

		// init log mq
		if err = initLogMQ(*config.MQConfig); err != nil {
			level.Error(logger).Log("init rabbitmq connection faileds. err=", err.Error())
			return err
		}
		deliveryChan, err := ch.Consume("q.freego_work_log.qa", "oklog", false, false, false, false, nil)
		if err != nil {
			level.Error(logger).Log("consume mq log failed. err=%s\n", err.Error())
			return err
		}
		var delivery amqp.Delivery
		for {
			// We enter the loop wanting to write s.Text() to the conn.
			delivery = <-deliveryChan
			record := fmt.Sprintf("[%s] %s\n", prefix, string(delivery.Body))
			fmt.Println(record)
			delivery.Ack(true)
			if n, err := fmt.Fprintf(conn, record); err != nil {
				metrics.Disconnects.Inc()
				level.Warn(logger).Log("disconnected_from", target.String(), "due_to", err)
				break
			} else if n < len(record) {
				metrics.ShortWriteBytes.Inc()
				level.Warn(logger).Log("short_write_to", target.String(), "n", n, "less_than", len(record))
				break // TODO(pb): we should do something more sophisticated here
			}

			// Only once the write succeeds do we scan the next record.
			backoff = 0 // reset the backoff on a successful write
			metrics.Bytes.Add(float64(len(record)))
			metrics.Records.Inc()
		}
	}
}

func exponential(d time.Duration) time.Duration {
	const (
		min = 16 * time.Millisecond
		max = 1024 * time.Millisecond
	)
	d *= 2
	if d < min {
		d = min
	}
	if d > max {
		d = max
	}
	return d
}

var (
	exchange   = "ex-freego_work_log-direct.qa"
	routingKey = "rk.freego_work_log.qa"
	conn       *amqp.Connection
	ch         *amqp.Channel
)

func initLogMQ(path string) (err error) {
	if conn, err = amqp.Dial(path); err != nil {
		fmt.Printf("init producer rabbitmq failed. err=%s\n", err.Error())
		return
	}
	if ch, err = conn.Channel(); err != nil {
		fmt.Printf("get rabbitmq channel failed. err=%s\n", err.Error())
		return
	}
	return
}
