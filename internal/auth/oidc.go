package auth

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

type oidcProvider struct {
	provider *oidc.Provider
	oauth2   oauth2.Config
	verifier *oidc.IDTokenVerifier
	cfg      *config.Config
}

func newOIDCProvider(ctx context.Context, cfg *config.Config) (*oidcProvider, error) {
	provider, err := oidc.NewProvider(ctx, cfg.Auth.OIDC.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	secret := os.Getenv(cfg.Auth.OIDC.ClientSecretEnv)
	redirect := strings.TrimRight(cfg.Server.PublicBaseURL, "/") + "/auth/callback"
	oauth2Cfg := oauth2.Config{
		ClientID:     cfg.Auth.OIDC.ClientID,
		ClientSecret: secret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  redirect,
		Scopes:       cfg.Auth.OIDC.Scopes,
	}
	return &oidcProvider{
		provider: provider,
		oauth2:   oauth2Cfg,
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.Auth.OIDC.ClientID}),
		cfg:      cfg,
	}, nil
}

func (p *oidcProvider) authCodeURL(state string) string {
	// PKCE would require storing verifier in cookie/session; use state for CSRF for MVP.
	return p.oauth2.AuthCodeURL(state, oauth2.AccessTypeOnline)
}

func (p *oidcProvider) exchange(ctx context.Context, s *Service, code string) (*sqlite.User, error) {
	tok, err := p.oauth2.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		return nil, fmt.Errorf("id_token missing from token response")
	}
	idTok, err := p.verifier.Verify(ctx, rawID)
	if err != nil {
		return nil, fmt.Errorf("verify id_token: %w", err)
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		Sub           string `json:"sub"`
	}
	if err := idTok.Claims(&claims); err != nil {
		return nil, fmt.Errorf("parse claims: %w", err)
	}
	if claims.Email == "" {
		return nil, fmt.Errorf("email claim required")
	}
	if claims.Sub == "" {
		claims.Sub = idTok.Subject
	}
	display := claims.Name
	if display == "" {
		display = claims.Email
	}
	return s.eng.Users().UpsertOIDC(claims.Email, display, claims.Sub, s.cfg.Auth.BootstrapAdminEmails)
}
