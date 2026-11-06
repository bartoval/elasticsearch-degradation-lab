package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// DocumentMultiplication selects a source index, copy count, and destination document limit.
type DocumentMultiplication struct {
	Source       string
	Copies       int
	MaxDocuments int
}

type multiplicationSource struct {
	Mappings json.RawMessage `json:"mappings"`
	Settings struct {
		Index map[string]json.RawMessage `json:"index"`
	} `json:"settings"`
}

type reindexResult struct {
	Created  int64             `json:"created"`
	TimedOut bool              `json:"timed_out"`
	Failures []json.RawMessage `json:"failures"`
}

// ApplyMultipliedDocuments copies a concrete index into a new index with distinct IDs per copy.
func (node *Node) ApplyMultipliedDocuments(
	ctx context.Context,
	indexName string,
	multiplication DocumentMultiplication,
	reportProgress func(string),
) error {
	var sourceIndices map[string]multiplicationSource
	sourcePath := "/" + url.PathEscape(multiplication.Source)
	if sourceError := node.requestJSON(ctx, http.MethodGet, sourcePath, nil, &sourceIndices); sourceError != nil {
		return fmt.Errorf("read multiplication source: %w", sourceError)
	}

	source, exists := sourceIndices[multiplication.Source]
	if !exists || len(sourceIndices) != 1 {
		return fmt.Errorf(
			"multiplication source %q must be a concrete index, not an alias or data stream",
			multiplication.Source,
		)
	}

	if refreshError := node.requestJSON(ctx, http.MethodPost, sourcePath+"/_refresh", nil, nil); refreshError != nil {
		return fmt.Errorf("refresh multiplication source: %w", refreshError)
	}

	var countResponse struct {
		Count  int64 `json:"count"`
		Shards struct {
			Failed int `json:"failed"`
		} `json:"_shards"`
	}
	if countError := node.requestJSON(ctx, http.MethodGet, sourcePath+"/_count", nil, &countResponse); countError != nil {
		return fmt.Errorf("count multiplication source: %w", countError)
	}

	if countResponse.Shards.Failed > 0 {
		return fmt.Errorf("multiplication source count failed on %d shard(s)", countResponse.Shards.Failed)
	}

	if countResponse.Count == 0 {
		return fmt.Errorf("multiplication source %q has no documents", multiplication.Source)
	}

	if countResponse.Count > int64(multiplication.MaxDocuments)/int64(multiplication.Copies) {
		return fmt.Errorf(
			"multiplication of %d documents by %d exceeds the %d document limit",
			countResponse.Count,
			multiplication.Copies,
			multiplication.MaxDocuments,
		)
	}

	settings := map[string]any{"number_of_shards": 1, "number_of_replicas": 0}
	for _, settingName := range []string{"analysis", "similarity", "mapping"} {
		if value, exists := source.Settings.Index[settingName]; exists {
			settings[settingName] = value
		}
	}

	indexPath := "/" + url.PathEscape(indexName)
	definition := map[string]any{"mappings": source.Mappings, "settings": settings}
	if createError := node.requestJSON(ctx, http.MethodPut, indexPath, definition, nil); createError != nil {
		return fmt.Errorf("create multiplication destination: %w", createError)
	}

	for copyNumber := 1; copyNumber <= multiplication.Copies; copyNumber++ {
		reportProgress(fmt.Sprintf(
			"Copying %s to %s (%d of %d, %d documents per copy)",
			multiplication.Source,
			indexName,
			copyNumber,
			multiplication.Copies,
			countResponse.Count,
		))
		body := map[string]any{
			"source": map[string]string{"index": multiplication.Source},
			"dest":   map[string]string{"index": indexName, "op_type": "create"},
			"script": map[string]any{
				"lang":   "painless",
				"source": "ctx._id = params.prefix + ctx._id",
				"params": map[string]string{"prefix": fmt.Sprintf("copy-%d:", copyNumber)},
			},
		}
		var started struct {
			Task string `json:"task"`
		}
		if startError := node.requestJSON(ctx, http.MethodPost, "/_reindex?wait_for_completion=false", body, &started); startError != nil {
			return fmt.Errorf("start document copy %d: %w", copyNumber, startError)
		}

		if started.Task == "" {
			return fmt.Errorf("document copy %d returned no reindex task", copyNumber)
		}

		if copyError := node.waitForDocumentCopy(ctx, started.Task, countResponse.Count, reportProgress); copyError != nil {
			return fmt.Errorf("document copy %d: %w", copyNumber, copyError)
		}
	}

	if refreshError := node.requestJSON(ctx, http.MethodPost, indexPath+"/_refresh", nil, nil); refreshError != nil {
		return fmt.Errorf("refresh multiplication destination: %w", refreshError)
	}

	return nil
}

func (node *Node) waitForDocumentCopy(ctx context.Context, taskID string, expectedDocuments int64, reportProgress func(string)) error {
	taskPath := "/_tasks/" + url.PathEscape(taskID)
	readinessTicker := time.NewTicker(readinessInterval)
	defer readinessTicker.Stop()

	progressTicker := time.NewTicker(progressInterval)
	defer progressTicker.Stop()

	for {
		var task struct {
			Completed bool            `json:"completed"`
			Error     json.RawMessage `json:"error"`
			Response  *reindexResult  `json:"response"`
		}
		taskError := node.requestJSON(ctx, http.MethodGet, taskPath, nil, &task)
		if ctx.Err() != nil {
			return errors.Join(ctx.Err(), node.cancelDocumentCopy(taskPath))
		}

		if taskError != nil {
			return fmt.Errorf("read reindex task: %w", taskError)
		}

		if task.Completed {
			if len(task.Error) > 0 && string(task.Error) != "null" {
				return fmt.Errorf("reindex task failed: %.4096s", task.Error)
			}

			if task.Response == nil {
				return fmt.Errorf("reindex task completed without a response")
			}

			if task.Response.TimedOut {
				return fmt.Errorf("reindex task timed out")
			}

			if len(task.Response.Failures) > 0 {
				return fmt.Errorf(
					"reindex reported %d failure(s): %.4096s",
					len(task.Response.Failures),
					task.Response.Failures[0],
				)
			}

			if task.Response.Created != expectedDocuments {
				return fmt.Errorf(
					"reindex created %d documents, expected %d",
					task.Response.Created,
					expectedDocuments,
				)
			}

			return nil
		}

		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), node.cancelDocumentCopy(taskPath))
		case <-progressTicker.C:
			reportProgress(fmt.Sprintf("Waiting for reindex task %s", taskID))
		case <-readinessTicker.C:
		}
	}
}

func (node *Node) cancelDocumentCopy(taskPath string) error {
	ctx, cancelRequest := context.WithTimeout(context.Background(), setupRequestTimeout)
	defer cancelRequest()

	if cancelError := node.requestJSON(ctx, http.MethodPost, taskPath+"/_cancel", nil, nil); cancelError != nil {
		return fmt.Errorf("cancel reindex task: %w", cancelError)
	}

	return nil
}
