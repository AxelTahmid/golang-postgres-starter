package auth

import (
	"net/http"

	"github.com/AxelTahmid/tinker/internal/httpx"
	"github.com/AxelTahmid/tinker/internal/jwt"
)

func (h *Handler) Routes() *httpx.Group {
	g := httpx.NewGroup(httpx.Defaults{Tags: []string{"auth"}})

	httpx.Register(g, httpx.Operation[LoginInput, jwt.Tokens]{
		Method:             http.MethodPost,
		Path:               "/login",
		Summary:            "Log in",
		BodyDescription:    "User credentials",
		SuccessDescription: "Authentication tokens issued",
		Success:            httpx.Enveloped(http.StatusOK),
		Problems: []httpx.ProblemKind{
			httpx.Unauthorized().Described("The credentials are invalid"),
		},
		Handler: h.Login,
	})

	httpx.Register(g, httpx.Operation[RegisterInput, httpx.NoBody]{
		Method:             http.MethodPost,
		Path:               "/register",
		Summary:            "Register an administrator",
		BodyDescription:    "New user details",
		SuccessDescription: "User registered",
		Success:            httpx.Message(http.StatusCreated),
		Problems: []httpx.ProblemKind{
			httpx.Conflict().Described("A user with this identity already exists"),
		},
		Handler: h.RegisterAdmin,
	})

	httpx.Register(g, httpx.Operation[EmptyInput, UserResponse]{
		Method:             http.MethodGet,
		Path:               "/me",
		Summary:            "Get the current user",
		SuccessDescription: "Current user returned",
		Success:            httpx.Enveloped(http.StatusOK),
		Problems: []httpx.ProblemKind{
			httpx.NotFound().Described("The authenticated user no longer exists"),
		},
		Guards:  []httpx.Guard{jwt.AccessGuard()},
		Handler: h.Me,
	})

	httpx.Register(g, httpx.Operation[EmptyInput, jwt.Tokens]{
		Method:             http.MethodPost,
		Path:               "/refresh",
		Summary:            "Refresh authentication tokens",
		SuccessDescription: "Fresh authentication tokens issued",
		Success:            httpx.Enveloped(http.StatusOK),
		Problems: []httpx.ProblemKind{
			httpx.Unauthorized().Described("The refresh identity is invalid"),
		},
		Guards:  []httpx.Guard{jwt.RefreshGuard()},
		Handler: h.Refresh,
	})

	return g
}
