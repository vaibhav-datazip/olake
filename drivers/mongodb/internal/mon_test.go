package driver

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/datazip-inc/olake/drivers/base"
	"github.com/datazip-inc/olake/protocol"
	"github.com/datazip-inc/olake/types"
	"github.com/datazip-inc/olake/writers/parquet"
	"github.com/stretchr/testify/assert"
)

func TestMongo_Setup(t *testing.T) {
	// Skip if not in test environment
	client, _ := testClient(t)
	assert.NotNil(t, client)
}

func TestMongo_Check(t *testing.T) {
	client, config := testClient(t)
	assert.NotNil(t, client)

	t.Run("successful connection check", func(t *testing.T) {
		mongoClient := &Mongo{
			Driver: base.NewBase(),
			client: client,
			config: &config,
		}
		err := mongoClient.Check()
		assert.NoError(t, err)
	})
}

func TestMongo_Discover(t *testing.T) {
	client, config := testClient(t)
	assert.NotNil(t, client)

	ctx := context.Background()

	t.Run("discover with collections", func(t *testing.T) {
		// Create test collection with sample data
		addTestTableData(
			ctx,
			t,
			client,
			config.Database,
			"test_collection",
			2,
			0,
			"col1", "col2",
		)
		mongoClient := &Mongo{
			Driver: base.NewBase(),
			client: client,
			config: &config,
		}
		streams, err := mongoClient.Discover(true)
		assert.NoError(t, err)
		assert.NotEmpty(t, streams)
		for _, stream := range streams {
			if stream.Name == "test_collection" {
				return
			}
		}
		assert.NoError(t, fmt.Errorf("unable to found test collection"))
	})
}

func TestMongo_Read(t *testing.T) {
	client, config := testClient(t)
	if client == nil {
		return
	}
	ctx := context.Background()
	collectionName := "test_read_collection"

	// Setup test data
	addTestTableData(
		ctx,
		t,
		client,
		config.Database,
		collectionName,
		5,
		0,
		"col1",
	)

	t.Run("full refresh read", func(t *testing.T) {
		mongoClient := &Mongo{
			Driver: base.NewBase(),
			client: client,
			config: &config,
		}
		protocol.RegisteredWriters[types.Parquet] = func() protocol.Writer {
			return &parquet.Parquet{}
		}
		// Create a mock writer pool
		pool, err := protocol.NewWriter(ctx, &types.WriterConfig{
			Type: "PARQUET",
			WriterConfig: map[string]any{
				"local_path": os.TempDir(),
			},
		})
		assert.NoError(t, err)
		// creating a dummy stream
		streams, err := mongoClient.Discover(true)
		assert.NoError(t, err)

		dummyStream := &types.ConfiguredStream{
			Stream: streams[0],
		}
		assert.NoError(t, err)
		assert.NotEmpty(t, streams)
		// run for cdc mode
		dummyStream.Stream.SyncMode = "full_refresh"
		dummyStream.SetupState(&types.State{})
		err = mongoClient.Read(pool, dummyStream)
		assert.NoError(t, err)
	})

	t.Run("cdc read", func(t *testing.T) {
		mongoClient := &Mongo{
			Driver: base.NewBase(),
			client: client,
			config: &config,
		}

		protocol.RegisteredWriters[types.Parquet] = func() protocol.Writer {
			return &parquet.Parquet{}
		}
		// Create a mock writer pool
		pool, err := protocol.NewWriter(ctx, &types.WriterConfig{
			Type: "PARQUET",
			WriterConfig: map[string]any{
				"local_path": os.TempDir(),
			},
		})
		assert.NoError(t, err)
		// creating a dummy stream
		streams, err := mongoClient.Discover(true)
		assert.NoError(t, err)

		dummyStream := &types.ConfiguredStream{
			Stream: streams[0],
		}
		assert.NoError(t, err)
		assert.NotEmpty(t, streams)
		// run for cdc mode
		dummyStream.Stream.SyncMode = "cdc"
		dummyStream.SetupState(&types.State{})
		err = mongoClient.Read(pool, dummyStream)
		// TODO: Replica set test add on
		assert.ErrorContains(t, err, "$changeStream stage is only supported on replica set")
	})
}
