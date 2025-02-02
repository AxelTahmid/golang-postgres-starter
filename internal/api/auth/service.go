package auth

import (
	"context"
	"errors"
	"log/slog"

	"github.com/AxelTahmid/tinker/internal/jwt"
	"github.com/AxelTahmid/tinker/internal/utils/ctxkeys"
	"github.com/AxelTahmid/tinker/internal/utils/util"
	"golang.org/x/crypto/bcrypt"
)

type Service interface {
	register(ctx context.Context, user *RegisterRequest) error
	login(ctx context.Context, credentials *LoginRequest) (*jwt.Tokens, error)
	me(ctx context.Context, email string) (interface{}, error)
	refresh(ctx context.Context, refreshToken string) (*jwt.Tokens, error)
}

type service struct {
	repo  Repository
	token *jwt.JWTManager
	log   *slog.Logger
}

func NewService(repo Repository) Service {
	l := slog.With(slog.String("layer", "auth-service"))
	return &service{repo: repo, token: jwt.Service, log: l}
}

func (s *service) register(ctx context.Context, user *RegisterRequest) error {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(user.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	user.Password = string(hashedPassword)

	switch role := user.Role; role {
	case "admin":
		return s.repo.createAdmin(ctx, user)

	case "tenant":
		return s.repo.createTenant(ctx, user)

	case "customer":
		tenantID := ctx.Value(ctxkeys.TenantID)
		if tenantID == nil {
			return errors.New("unknown tenant in customer")
		}

		return s.repo.createCustomer(ctx, user)

	default:
		return errors.New("unknown action taken")
	}
}

func (s *service) login(ctx context.Context, credentials *LoginRequest) (*jwt.Tokens, error) {
	user, err := s.repo.findUser(ctx, credentials.Email)
	if err != nil {
		// return nil, errors.New("invalid credentials")
		s.log.Error(err.Error())
		return nil, errors.New("invalid credentials")
	}

	err = bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(credentials.Password))
	if err != nil {
		s.log.Error(err.Error())
		return nil, errors.New("invalid credentials")
	}

	userClaims := jwt.UserClaims{
		Id:    user.ID,
		Email: user.Email,
		Role:  string(user.Role),
	}

	return s.token.IssueTokenPair(&userClaims)
}

func (s *service) me(ctx context.Context, email string) (interface{}, error) {
	user, err := s.repo.getUserDetails(ctx, email)
	if err != nil {
		return user, err
	}

	return util.AddOmitemptyToJSONTags(user), nil
	// return user, nil
}

func (s *service) refresh(ctx context.Context, email string) (*jwt.Tokens, error) {
	user, err := s.repo.findUser(ctx, email)
	if err != nil {
		return nil, errors.New("user not found")
	}

	// Issue a new access token
	tokens, err := s.token.IssueTokenPair(&jwt.UserClaims{
		Id:    user.ID,
		Email: user.Email,
		Role:  string(user.Role),
	})
	if err != nil {
		return nil, errors.New("failed to generate access token")
	}

	return tokens, nil
}
