package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/11DingKing/urban-sports-safety-hub/internal/auth"
	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	dbstore "github.com/11DingKing/urban-sports-safety-hub/internal/storage/sqlite"
)

func Administrator(ctx context.Context, store *dbstore.Store, service *auth.Service) error {
	email := strings.TrimSpace(os.Getenv("BOOTSTRAP_ADMIN_EMAIL"))
	password := os.Getenv("BOOTSTRAP_ADMIN_PASSWORD")
	if email == "" && password == "" {
		return nil
	}
	if email == "" || password == "" {
		return fmt.Errorf("BOOTSTRAP_ADMIN_EMAIL and BOOTSTRAP_ADMIN_PASSWORD must be provided together")
	}
	_, err := store.AccountByEmail(ctx, email)
	if err == nil {
		return nil
	}
	var typed *domain.Error
	if !errors.As(err, &typed) || typed.Code != "bad_credentials" {
		return fmt.Errorf("check bootstrap administrator: %w", err)
	}
	_, err = service.Register(ctx, email, password, "Platform Administrator", domain.RoleAdministrator)
	if err != nil {
		return fmt.Errorf("create bootstrap administrator: %w", err)
	}
	return nil
}

func DemoData(ctx context.Context, db *sql.DB) error {
	if os.Getenv("BOOTSTRAP_DEMO_DATA") != "true" {
		return nil
	}
	statements := []string{
		`INSERT OR IGNORE INTO course_templates(id,name,sport,level,minimum_age,capacity,coach_ratio,required_certification) VALUES(1,'Climbing Foundations','climbing',1,8,12,6,'')`,
		`INSERT OR IGNORE INTO course_templates(id,name,sport,level,minimum_age,capacity,coach_ratio,required_certification) VALUES(2,'Skate Park Safety','skateboarding',1,10,10,5,'')`,
		`INSERT OR IGNORE INTO course_templates(id,name,sport,level,minimum_age,capacity,coach_ratio,required_certification) VALUES(3,'Flying Disc Team Skills','flying_disc',2,9,16,8,'flying_disc:1')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("insert demo reference data: %w", err)
		}
	}
	return nil
}
