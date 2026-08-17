package auth

import (
	"context"

	"github.com/AxelTahmid/tinker/internal/httpx"
	"github.com/AxelTahmid/tinker/internal/jwt"
)

type Handler struct {
	svc Service
}

func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) RegisterAdmin(ctx context.Context, req *RegisterInput) (*httpx.Reply[httpx.NoBody], error) {
	if err := h.svc.RegisterAdmin(ctx, &req.Body); err != nil {
		return nil, err
	}
	return httpx.Done("User registered successfully"), nil
}

func (h *Handler) Login(ctx context.Context, req *LoginInput) (*httpx.Reply[jwt.Tokens], error) {
	tokens, err := h.svc.Login(ctx, &req.Body)
	if err != nil {
		return nil, err
	}
	return httpx.OK("Login successful", *tokens), nil
}

func (h *Handler) Me(ctx context.Context, _ *EmptyInput) (*httpx.Reply[UserResponse], error) {
	claims, _, err := jwt.ParseClaimsCtx(ctx)
	if err != nil {
		return nil, httpx.NewUnauthorizedError("authentication required")
	}
	user, err := h.svc.Me(ctx, claims.Subject)
	if err != nil {
		return nil, err
	}
	return httpx.OK("User details fetched successfully", userResponse(user)), nil
}

func (h *Handler) Refresh(ctx context.Context, _ *EmptyInput) (*httpx.Reply[jwt.Tokens], error) {
	claims, _, err := jwt.ParseClaimsCtx(ctx)
	if err != nil {
		return nil, httpx.NewUnauthorizedError("authentication required")
	}
	tokens, err := h.svc.Refresh(ctx, claims.Subject)
	if err != nil {
		return nil, err
	}
	return httpx.OK("Token refreshed successfully", *tokens), nil
}
