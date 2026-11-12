// Package metrics registers all Prometheus collectors for the gateway.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	PaymentsCreated = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "payments_created_total", Help: "Payments created.",
	}, []string{"currency"})
	PaymentsConfirmed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "payments_confirmed_total", Help: "Payments confirmed on-chain."})
	PaymentsFailed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "payments_failed_total", Help: "Payments failed validation.",
	}, []string{"reason"})
	PaymentsExpired = promauto.NewCounter(prometheus.CounterOpts{
		Name: "payments_expired_total", Help: "Payments expired unclaimed."})
	WebhookAttempts = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "webhook_attempts_total", Help: "Webhook delivery attempts.",
	}, []string{"result"})
	WebhookDeliverySeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name: "webhook_delivery_seconds", Help: "Webhook delivery latency.",
		Buckets: prometheus.ExponentialBuckets(0.01, 2, 10)})
	RPCRequestSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "rpc_request_seconds", Help: "RPC call latency.",
		Buckets: prometheus.ExponentialBuckets(0.01, 2, 10),
	}, []string{"method"})
	RPCErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rpc_errors_total", Help: "RPC call errors.",
	}, []string{"method"})
)
