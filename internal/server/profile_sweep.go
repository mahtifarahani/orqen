package server

import (
	"context"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"orqen/internal/catherd"
)

// sweepAbandoned marks publishing versions whose job is no longer active as failed. Filter limits it to one profile
// when profileID is not empty (publish time) or covers every organization (server start).
func (a *App) sweepAbandoned(ctx context.Context, org, profileID string) error {
	filter := bson.M{"state": "publishing"}
	if org != "" {
		filter["org_id"] = org
	}
	if profileID != "" {
		filter["profile_id"] = profileID
	}
	stuck, err := findMany[ModelProfileVersionDoc](ctx, a.DB, "model_profile_versions", filter, bson.D{{Key: "_id", Value: 1}}, 1000)
	if err != nil {
		return err
	}
	for _, v := range stuck {
		active, err := a.DB.Coll("jobs").CountDocuments(ctx, bson.M{"org_id": v.OrgID, "kind": "profile_publish", "operation_id": v.OperationID, "active": true})
		if err != nil {
			return err
		}
		if active > 0 {
			continue
		}
		if _, err = a.DB.Coll("model_profile_versions").UpdateOne(ctx, bson.M{"_id": v.ID, "state": "publishing"}, bson.M{"$set": bson.M{"state": "failed", "failure": VersionFailure{Code: "publish_abandoned", Message: "the publish job ended without finishing", Issues: []catherd.Issue{}}}}); err != nil && err != mongo.ErrNoDocuments {
			return err
		}
	}
	return nil
}
