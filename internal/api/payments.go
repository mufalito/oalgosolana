package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mufalito/oalgosolana/internal/apierr"
	"github.com/mufalito/oalgosolana/internal/payment"
)

// mountPayments wires the payment endpoints. Called by NewServer.
func (s *Server) mountPayments(r chi.Router) {
	h := &paymentsHandler{svc: s.Payments, replayer: s.Replayer}
	r.Post("/api/v1/payments", h.create)
	r.Get("/api/v1/payments", h.list)
	r.Post("/api/v1/payment-links", h.createLink)
	r.Post("/api/v1/payment-links/{id}/redeem", h.redeemLink)
	r.Get("/api/v1/payments/{id}", h.get)
	if s.Replayer != nil {
		r.Post("/api/v1/payments/{id}/webhook/replay", h.replay)
	}
}

type paymentsHandler struct {
	svc      PaymentsService
	replayer func(ctx context.Context, paymentID string) error
}

// PaymentsService is the subset of payment.Service the handlers need;
type PaymentsService interface {
	Create(ctx context.Context, req payment.CreateRequest) (*payment.Created, error)
	GetByID(ctx context.Context, id string) (*payment.Get, error)
	ListRecent(ctx context.Context) ([]*payment.Get, error)
	CreateLink(ctx context.Context, in payment.CreateLinkInput) (*payment.LinkCreated, error)
	RedeemLink(ctx context.Context, id string) (*payment.Created, error)
}

type createPaymentRequest struct {
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	OrderID       string `json:"order_id"`
	Label         string `json:"label"`
	Message       string `json:"message"`
	WebhookURL    string `json:"webhook_url"`
	ExpirationMin *int   `json:"expiration_minutes"`
}

func (h *paymentsHandler) create(w http.ResponseWriter, r *http.Request) {
	var body createPaymentRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		apierr.WriteError(w, apierr.Errorf(http.StatusBadRequest, apierr.CodeInvalidRequest, "invalid JSON body: %v", err))
		return
	}
	req := payment.CreateRequest{
		Amount:     body.Amount,
		Currency:   body.Currency,
		OrderID:    body.OrderID,
		Label:      body.Label,
		Message:    body.Message,
		WebhookURL: body.WebhookURL,
	}
	if body.ExpirationMin != nil {
		req.ExpirationMinSet = true
		req.ExpirationMin = *body.ExpirationMin
	}
	created, err := h.svc.Create(r.Context(), req)
	if err != nil {
		apierr.WriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, created)
}

type createLinkRequest struct {
	Amount          string `json:"amount"`
	Currency        string `json:"currency"`
	Label           string `json:"label"`
	Message         string `json:"message"`
	ExpirationMin   *int   `json:"expiration_minutes"`
	LinkExpiresDays int    `json:"link_expires_days"`
}

func (h *paymentsHandler) createLink(w http.ResponseWriter, r *http.Request) {
	var body createLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apierr.WriteError(w, apierr.Errorf(400, apierr.CodeInvalidRequest, "invalid JSON body"))
		return
	}
	in := payment.CreateLinkInput{
		Amount: body.Amount, Currency: body.Currency, Label: body.Label, Message: body.Message,
		LinkExpiresDays: body.LinkExpiresDays,
	}
	if body.ExpirationMin != nil {
		in.ExpirationMinSet = true
		in.ExpirationMin = *body.ExpirationMin
	}
	link, err := h.svc.CreateLink(r.Context(), in)
	if err != nil {
		apierr.WriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, link)
}

func (h *paymentsHandler) redeemLink(w http.ResponseWriter, r *http.Request) {
	created, err := h.svc.RedeemLink(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		apierr.WriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, created)
}

func (h *paymentsHandler) list(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListRecent(r.Context())
	if err != nil {
		apierr.WriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *paymentsHandler) get(w http.ResponseWriter, r *http.Request) {
	got, err := h.svc.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		apierr.WriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *paymentsHandler) replay(w http.ResponseWriter, r *http.Request) {
	if err := h.replayer(r.Context(), chi.URLParam(r, "id")); err != nil {
		apierr.WriteError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "requeued"})
}
