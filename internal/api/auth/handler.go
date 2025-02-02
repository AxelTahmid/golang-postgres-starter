package auth

import (
	"net/http"

	"github.com/AxelTahmid/tinker/internal/jwt"
	"github.com/AxelTahmid/tinker/internal/utils/message"
	"github.com/AxelTahmid/tinker/internal/utils/request"
	"github.com/AxelTahmid/tinker/internal/utils/respond"
	"github.com/AxelTahmid/tinker/internal/utils/validate"
)

type Handler struct {
	svc Service
	v   *validate.Validate
}

func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc, v: validate.New()}
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var user RegisterRequest
	if err := request.DecodeJSON(w, r, &user); err != nil {
		respond.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := validate.New().Check(&user); err != nil {
		respond.ValidationError(w, err)
		return
	}

	if err := h.svc.register(r.Context(), &user); err != nil {
		respond.Error(w, http.StatusConflict, err.Error())
		return
	}

	respond.Success(w, http.StatusCreated, "User registered successfully", nil)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {

	var credentials LoginRequest
	if err := request.DecodeJSON(w, r, &credentials); err != nil {
		respond.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.v.Check(&credentials); err != nil {
		respond.ValidationError(w, err)
		return
	}

	tokens, err := h.svc.login(r.Context(), &credentials)
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, err.Error())
		return
	}

	respond.Success(w, http.StatusOK, "Login successful", tokens)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {

	ctx := r.Context()

	userClaim, err := jwt.GetClaimsFromContext(ctx)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, message.ErrBadRequest)
		return
	}

	// Retrieve user details
	user, err := h.svc.me(ctx, userClaim.Subject)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "User not found")

		return
	}
	// user.AuthUser.Password = ""
	respond.Success(w, http.StatusOK, "User details fetched successfully", user)
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {

	ctx := r.Context()

	userClaim, err := jwt.GetClaimsFromContext(ctx)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, message.ErrBadRequest)
		return
	}

	tokens, err := h.svc.refresh(ctx, userClaim.Subject)
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "Invalid or expired refresh token")
		return
	}

	respond.Success(w, http.StatusOK, "Token refreshed successfully", tokens)
}
