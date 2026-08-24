package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	dbstore "github.com/11DingKing/urban-sports-safety-hub/internal/storage/sqlite"
)

type Service struct{ store *dbstore.Store }

func New(store *dbstore.Store) *Service { return &Service{store: store} }
func (s *Service) Record(ctx context.Context, tx *sql.Tx, actorID int64, requestID, objectType string, objectID int64, action, result string, detail any) error {
	encoded, err := json.Marshal(detail)
	if err != nil {
		return domain.Wrap(domain.KindInvalid, "invalid_audit_detail", "audit detail cannot be encoded", err)
	}
	actor := actorID
	_, err = s.store.AppendAudit(ctx, tx, domain.AuditEvent{ActorID: &actor, RequestID: requestID, ObjectType: objectType, ObjectID: objectID, Action: action, Result: result, Detail: string(encoded)})
	return err
}
