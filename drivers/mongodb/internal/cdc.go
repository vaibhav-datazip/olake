package driver

import (
	"context"
	"fmt"

	"github.com/datazip-inc/olake/constants"
	"github.com/datazip-inc/olake/logger"
	"github.com/datazip-inc/olake/protocol"
	"github.com/datazip-inc/olake/types"
	"github.com/datazip-inc/olake/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
)

type CDCDocument struct {
	OperationType string         `json:"operationType"`
	FullDocument  map[string]any `json:"fullDocument"`
}

func (m *Mongo) RunChangeStream(ctx context.Context, pool *protocol.WriterPool, streams ...protocol.Stream) error {
	// TODO: concurrency based on configuration
	return utils.Concurrent(context.TODO(), streams, len(streams), func(ctx context.Context, stream protocol.Stream, executionNumber int) error {
		return m.changeStreamSync(ctx, stream, pool)
	})
}

func (m *Mongo) SetupGlobalState(state *types.State) error {
	// mongo db does not support any global state
	// stream level states can be used
	return nil
}

func (m *Mongo) StateType() types.StateType {
	return types.StreamType
}

// does full load on empty state
func (m *Mongo) changeStreamSync(cdcCtx context.Context, stream protocol.Stream, pool *protocol.WriterPool) error {
	collection := m.client.Database(stream.Namespace(), options.Database().SetReadConcern(readconcern.Majority())).Collection(stream.Name())
	changeStreamOpts := options.ChangeStream().SetFullDocument(options.UpdateLookup)
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.D{
			{Key: "operationType", Value: bson.D{{Key: "$in", Value: bson.A{"insert", "update", "delete"}}}},
		}}},
	}

	prevResumeToken := m.State.GetCursor(stream.Self(), cdcCursorField)
	chunks := m.State.GetChunks(stream.Self())

	if prevResumeToken == nil || chunks == nil || chunks.Len() != 0 {
		// get current resume token and do full load for stream
		resumeToken, err := m.getCurrentResumeToken(cdcCtx, collection, pipeline)
		if err != nil {
			return err
		}
		if resumeToken != nil {
			prevResumeToken = (*resumeToken).Lookup(cdcCursorField).StringValue()
		}

		// save resume token
		m.State.SetCursor(stream.Self(), cdcCursorField, prevResumeToken)

		if err := m.backfill(cdcCtx, pool, stream); err != nil {
			return err
		}
		logger.Infof("backfill done for stream[%s]", stream.ID())
	}

	changeStreamOpts = changeStreamOpts.SetResumeAfter(map[string]any{cdcCursorField: prevResumeToken})
	// resume cdc sync from prev resume token
	logger.Infof("Starting CDC sync for stream[%s] with resume token[%s]", stream.ID(), prevResumeToken)

	cursor, err := collection.Watch(cdcCtx, pipeline, changeStreamOpts)
	if err != nil {
		return fmt.Errorf("failed to open change stream: %s", err)
	}
	defer cursor.Close(cdcCtx)

	insert, err := pool.NewThread(cdcCtx, stream)
	if err != nil {
		return err
	}
	defer insert.Close()
	// Iterates over the cursor to print the change stream events
	for cursor.TryNext(cdcCtx) {
		var record CDCDocument
		if err := cursor.Decode(&record); err != nil {
			return fmt.Errorf("error while decoding: %s", err)
		}
		// TODO: Handle Deleted documents (Good First Issue)
		if record.FullDocument != nil {
			record.FullDocument["cdc_type"] = record.OperationType
		}
		handleObjectID(record.FullDocument)
		rawRecord := types.CreateRawRecord(utils.GetKeysHash(record.FullDocument, constants.MongoPrimaryID), record.FullDocument, 0)
		err := insert.Insert(rawRecord)
		if err != nil {
			return err
		}

		prevResumeToken = cursor.ResumeToken().Lookup(cdcCursorField).StringValue()
	}
	if err := cursor.Err(); err != nil {
		return fmt.Errorf("failed to iterate change streams cursor: %s", err)
	}

	// save state for the current stream
	m.State.SetCursor(stream.Self(), cdcCursorField, prevResumeToken)
	return nil
}

func (m *Mongo) getCurrentResumeToken(cdcCtx context.Context, collection *mongo.Collection, pipeline []bson.D) (*bson.Raw, error) {
	cursor, err := collection.Watch(cdcCtx, pipeline, options.ChangeStream())
	if err != nil {
		return nil, fmt.Errorf("failed to open change stream: %v", err)
	}
	defer cursor.Close(cdcCtx)

	resumeToken := cursor.ResumeToken()
	return &resumeToken, nil
}
