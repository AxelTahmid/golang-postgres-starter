package auth

import (
	"net/http"

	"github.com/AxelTahmid/tinker/internal/httpx"
	"github.com/AxelTahmid/tinker/internal/jwt"
)

type Handler struct {
	svc Service
}

func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.ParseRequest[*RegisterRequest](w, r)
	if err != nil {
		return
	}

	if err = h.svc.Register(r.Context(), req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	httpx.Success(w, http.StatusCreated, "User registered successfully", nil, nil)
}

func (h *Handler) RegisterAdmin(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.ParseRequest[*RegisterRequest](w, r)
	if err != nil {
		return
	}

	if err = h.svc.RegisterAdmin(r.Context(), req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	httpx.Success(w, http.StatusCreated, "User registered successfully", nil, nil)
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.ParseRequest[*LoginRequest](w, r)
	if err != nil {
		return
	}

	tokens, err := h.svc.Login(r.Context(), req)
	if err != nil {
		httpx.Error(w, http.StatusConflict, err.Error())
		return
	}

	httpx.Success(w, http.StatusOK, "Login successful", tokens, nil)
}

// TODO: remove helcim api token from response
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	userClaim, _, err := jwt.ParseClaimsCtx(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	// Retrieve user details
	user, err := h.svc.Me(r.Context(), userClaim.Subject)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, err.Error())
		return
	}

	httpx.Success(w, http.StatusOK, "User details fetched successfully", user, nil)
}

func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	userClaim, _, err := jwt.ParseClaimsCtx(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	tokens, err := h.svc.Refresh(r.Context(), userClaim.Subject)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}

	httpx.Success(w, http.StatusOK, "Token refreshed successfully", tokens, nil)
}
