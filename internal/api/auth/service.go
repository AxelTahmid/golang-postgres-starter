package auth

import (
	"context"
	"log/slog"

	"github.com/AxelTahmid/tinker/internal/db"
	"github.com/AxelTahmid/tinker/internal/db/sqlc"
	"github.com/AxelTahmid/tinker/internal/httpx"
	"github.com/AxelTahmid/tinker/internal/jwt"
	"github.com/AxelTahmid/tinker/pkg/argon2id"
)

type Service interface {
	RegisterAdmin(ctx context.Context, user *RegisterRequest) error
	Login(ctx context.Context, credentials *LoginRequest) (*jwt.Tokens, error)
	Me(ctx context.Context, email string) (sqlc.AuthUser, error)
	Refresh(ctx context.Context, refreshToken string) (*jwt.Tokens, error)
}

type service struct {
	repo  Repository
	token jwt.Service
	argon *argon2id.Config
}

func NewService(repo Repository) Service {
	return &service{repo: repo, token: jwt.GetService(), argon: argon2id.DefaultConfig()}
}

func (s *service) RegisterAdmin(ctx context.Context, user *RegisterRequest) error {
	hashedPassword, err := s.argon.HashPassword(user.Password)
	if err != nil {
		slog.ErrorContext(ctx, "failed to hash user password", "error", err)
		return httpx.NewInternalError("could not register user")
	}
	toCreate := *user
	toCreate.Password = hashedPassword
	if err = s.repo.CreateAdmin(ctx, &toCreate); err != nil {
		slog.ErrorContext(ctx, "failed to create user", "error", err)
		mapped := db.WrapDBError(ctx, err)
		if db.IsAlreadyExistsError(mapped) {
			return httpx.NewConflictError("a user with this identity already exists")
		}
		return httpx.NewInternalError("could not register user")
	}
	return nil
}

func (s *service) Login(ctx context.Context, credentials *LoginRequest) (*jwt.Tokens, error) {
	user, err := s.repo.FindUser(ctx, credentials.Identity)
	if err != nil {
		slog.WarnContext(ctx, "login identity lookup failed", "error", err)
		return nil, httpx.NewUnauthorizedError("invalid credentials")
	}
	match, err := s.argon.ComparePasswordAndHash(credentials.Password, user.Password)
	if err != nil {
		slog.ErrorContext(ctx, "failed to verify password", "error", err)
		return nil, httpx.NewInternalError("could not authenticate")
	}
	if !match {
		return nil, httpx.NewUnauthorizedError("invalid credentials")
	}
	return s.issueTokens(ctx, user)
}

func (s *service) Me(ctx context.Context, identity string) (sqlc.AuthUser, error) {
	user, err := s.repo.GetUserDetails(ctx, identity)
	if err != nil {
		slog.ErrorContext(ctx, "failed to load current user", "error", err)
		if db.IsNotFoundError(db.WrapDBError(ctx, err)) {
			return sqlc.AuthUser{}, httpx.NewNotFoundError("user does not exist")
		}
		return sqlc.AuthUser{}, httpx.NewInternalError("could not load user")
	}
	return user, nil
}

func (s *service) Refresh(ctx context.Context, identity string) (*jwt.Tokens, error) {
	user, err := s.repo.FindUser(ctx, identity)
	if err != nil {
		slog.WarnContext(ctx, "refresh identity lookup failed", "error", err)
		return nil, httpx.NewUnauthorizedError("refresh token is no longer valid")
	}
	return s.issueTokens(ctx, user)
}

func (s *service) issueTokens(ctx context.Context, user *sqlc.AuthUser) (*jwt.Tokens, error) {
	tokens, err := s.token.IssueTokenPair(user.ID, user.Email, string(user.Role))
	if err != nil {
		slog.ErrorContext(ctx, "failed to issue tokens", "error", err)
		return nil, httpx.NewInternalError("could not issue tokens")
	}
	return tokens, nil
}
