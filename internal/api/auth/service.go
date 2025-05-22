package auth

import (
	"context"
	"errors"

	"github.com/AxelTahmid/tinker/internal/db"
	"github.com/AxelTahmid/tinker/internal/db/sqlc"
	"github.com/AxelTahmid/tinker/internal/jwt"
	"github.com/AxelTahmid/tinker/internal/utils/argon2id"
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
	return &service{
		repo:  repo,
		token: jwt.GetService(),
		argon: argon2id.DefaultConfig(),
	}
}

func (s *service) RegisterAdmin(ctx context.Context, user *RegisterRequest) error {
	hashedPassword, err := s.argon.HashPassword(user.Password)
	if err != nil {
		return err
	}

	user.Password = hashedPassword

	if err = s.repo.CreateAdmin(ctx, user); err != nil {
		return db.WrapDBError(ctx, err)
	}

	return nil
}

func (s *service) Login(ctx context.Context, credentials *LoginRequest) (*jwt.Tokens, error) {
	user, err := s.repo.FindUser(ctx, credentials.Identity)
	if err != nil {
		return nil, db.WrapDBError(ctx, err)
	}

	match, err := s.argon.ComparePasswordAndHash(credentials.Password, user.Password)
	if err != nil {
		return nil, err
	}

	if !match {
		return nil, errors.New("invalid credentials")
	}

	return s.token.IssueTokenPair(
		user.ID,
		user.Email,
		string(user.Role),
	)
}

func (s *service) Me(ctx context.Context, identity string) (sqlc.AuthUser, error) {
	user, err := s.repo.GetUserDetails(ctx, identity)
	if err != nil {
		return sqlc.AuthUser{}, db.WrapDBError(ctx, err)
	}

	return user, nil
}

func (s *service) Refresh(ctx context.Context, identity string) (*jwt.Tokens, error) {
	user, err := s.repo.FindUser(ctx, identity)
	if err != nil {
		return nil, db.WrapDBError(ctx, err)
	}

	// Issue a new access token
	return s.token.IssueTokenPair(
		user.ID,
		user.Email,
		string(user.Role),
	)
}
