package metrics

import (
	"context"
	"os"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/tx7do/kratos-bootstrap/bootstrap"

	commonMetrics "github.com/go-tangra/go-tangra-common/metrics"
)

const namespace = "tangra"
const subsystem = "ticket"

// Collector holds Prometheus metrics for the ticket module.
type Collector struct {
	log    *log.Helper
	server *commonMetrics.MetricsServer

	TicketsByStatus *prometheus.GaugeVec
	TicketsCreated  prometheus.Counter
}

func NewCollector(ctx *bootstrap.Context) *Collector {
	c := &Collector{
		log: ctx.NewLoggerHelper("ticket/metrics"),
		TicketsByStatus: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "tickets_by_status",
			Help:      "Number of tickets by status.",
		}, []string{"status"}),
		TicketsCreated: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "tickets_created_total",
			Help:      "Total number of tickets created.",
		}),
	}

	prometheus.MustRegister(c.TicketsByStatus, c.TicketsCreated)

	addr := os.Getenv("METRICS_ADDR")
	if addr == "" {
		addr = ":10810"
	}
	c.server = commonMetrics.NewMetricsServer(addr, nil, ctx.GetLogger())

	go func() {
		if err := c.server.Start(); err != nil {
			c.log.Errorf("Metrics server failed: %v", err)
		}
	}()

	return c
}

func (c *Collector) TicketCreated(status string) {
	c.TicketsCreated.Inc()
	c.TicketsByStatus.WithLabelValues(status).Inc()
}

func (c *Collector) TicketStatusChanged(oldStatus, newStatus string) {
	if oldStatus != "" {
		c.TicketsByStatus.WithLabelValues(oldStatus).Dec()
	}
	c.TicketsByStatus.WithLabelValues(newStatus).Inc()
}

func (c *Collector) SetStatusCount(status string, n int) {
	c.TicketsByStatus.WithLabelValues(status).Set(float64(n))
}

func (c *Collector) Stop(ctx context.Context) {
	if c.server != nil {
		c.server.Stop(ctx)
	}
}
