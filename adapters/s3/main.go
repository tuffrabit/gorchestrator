// S3 storage adapter (JSON-RPC over stdio).
// Env: AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION, S3_BUCKET,
// optional S3_PREFIX, S3_ENDPOINT (MinIO).
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      *int64          `json:"id"`
}

type response struct {
	JSONRPC string    `json:"jsonrpc"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
	ID      *int64    `json:"id"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	bucket := os.Getenv("S3_BUCKET")
	prefix := strings.Trim(os.Getenv("S3_PREFIX"), "/")
	endpoint := os.Getenv("S3_ENDPOINT")

	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aws config: %v\n", err)
		os.Exit(1)
	}
	var client *s3.Client
	if endpoint != "" {
		client = s3.NewFromConfig(cfg, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		})
	} else {
		client = s3.NewFromConfig(cfg)
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	enc := json.NewEncoder(os.Stdout)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		resp := handle(ctx, client, bucket, prefix, req)
		_ = enc.Encode(resp)
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "scan: %v\n", err)
		os.Exit(1)
	}
}

func key(prefix, path string) string {
	path = strings.TrimPrefix(path, "/")
	if prefix == "" {
		return path
	}
	if path == "" {
		return prefix
	}
	return prefix + "/" + path
}

func handle(ctx context.Context, client *s3.Client, bucket, prefix string, req request) response {
	if bucket == "" {
		return errResp(req.ID, -32000, "S3_BUCKET not set")
	}
	switch req.Method {
	case "initialize":
		return response{JSONRPC: "2.0", Result: map[string]any{"ok": true, "port": "storage"}, ID: req.ID}
	case "storage.read":
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, -32602, err.Error())
		}
		out, err := client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key(prefix, p.Path)),
		})
		if err != nil {
			return response{JSONRPC: "2.0", Result: map[string]any{"exists": false, "content_str": ""}, ID: req.ID}
		}
		defer out.Body.Close()
		data, err := io.ReadAll(out.Body)
		if err != nil {
			return errResp(req.ID, -32000, err.Error())
		}
		return response{JSONRPC: "2.0", Result: map[string]any{"exists": true, "content_str": string(data), "size": len(data)}, ID: req.ID}
	case "storage.write":
		var p struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, -32602, err.Error())
		}
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key(prefix, p.Path)),
			Body:   strings.NewReader(p.Content),
		})
		if err != nil {
			return errResp(req.ID, -32000, err.Error())
		}
		return response{JSONRPC: "2.0", Result: map[string]any{"ok": true}, ID: req.ID}
	case "storage.exists":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(req.Params, &p)
		_, err := client.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key(prefix, p.Path)),
		})
		return response{JSONRPC: "2.0", Result: map[string]any{"exists": err == nil}, ID: req.ID}
	case "storage.list":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(req.Params, &p)
		pref := key(prefix, p.Path)
		if pref != "" && !strings.HasSuffix(pref, "/") {
			pref += "/"
		}
		out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:    aws.String(bucket),
			Prefix:     aws.String(pref),
			Delimiter: aws.String("/"),
		})
		if err != nil {
			return errResp(req.ID, -32000, err.Error())
		}
		type entry struct {
			Name  string `json:"name"`
			IsDir bool   `json:"is_dir"`
			Size  int64  `json:"size"`
		}
		var entries []entry
		for _, cp := range out.CommonPrefixes {
			name := strings.TrimPrefix(aws.ToString(cp.Prefix), pref)
			name = strings.TrimSuffix(name, "/")
			entries = append(entries, entry{Name: name, IsDir: true})
		}
		for _, obj := range out.Contents {
			name := strings.TrimPrefix(aws.ToString(obj.Key), pref)
			if name == "" {
				continue
			}
			entries = append(entries, entry{Name: name, IsDir: false, Size: aws.ToInt64(obj.Size)})
		}
		return response{JSONRPC: "2.0", Result: map[string]any{"entries": entries}, ID: req.ID}
	case "storage.mkdir":
		// S3 has no directories; write a placeholder key.
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(req.Params, &p)
		k := key(prefix, p.Path)
		if !strings.HasSuffix(k, "/") {
			k += "/"
		}
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(k + ".keep"),
			Body:   strings.NewReader(""),
		})
		if err != nil {
			return errResp(req.ID, -32000, err.Error())
		}
		return response{JSONRPC: "2.0", Result: map[string]any{"ok": true}, ID: req.ID}
	case "storage.remove_all":
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, -32602, err.Error())
		}
		rel := strings.TrimSpace(strings.Trim(p.Path, "/"))
		if rel == "" || rel == "." {
			return errResp(req.ID, -32602, "refusing to remove storage root")
		}
		pref := key(prefix, p.Path)
		if pref != "" && !strings.HasSuffix(pref, "/") {
			// Delete both the path as a key and everything under path/.
			// Issue dirs are always prefixes, but files may be keys too.
		}
		listPref := pref
		if listPref != "" && !strings.HasSuffix(listPref, "/") {
			listPref += "/"
		}
		var toDelete []types.ObjectIdentifier
		var token *string
		for {
			out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
				Bucket:            aws.String(bucket),
				Prefix:             aws.String(listPref),
				ContinuationToken: token,
			})
			if err != nil {
				return errResp(req.ID, -32000, err.Error())
			}
			for _, obj := range out.Contents {
				toDelete = append(toDelete, types.ObjectIdentifier{Key: obj.Key})
			}
			// Also try exact key match (file rather than prefix).
			if pref != "" && !strings.HasSuffix(pref, "/") {
				toDelete = append(toDelete, types.ObjectIdentifier{Key: aws.String(pref)})
			}
			if !aws.ToBool(out.IsTruncated) {
				break
			}
			token = out.NextContinuationToken
		}
		// Deduplicate keys.
		seen := map[string]struct{}{}
		var unique []types.ObjectIdentifier
		for _, id := range toDelete {
			k := aws.ToString(id.Key)
			if k == "" {
				continue
			}
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			unique = append(unique, id)
		}
		for i := 0; i < len(unique); i += 1000 {
			end := i + 1000
			if end > len(unique) {
				end = len(unique)
			}
			batch := unique[i:end]
			if len(batch) == 0 {
				continue
			}
			_, err := client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(bucket),
				Delete: &types.Delete{Objects: batch, Quiet: aws.Bool(true)},
			})
			if err != nil {
				return errResp(req.ID, -32000, err.Error())
			}
		}
		return response{JSONRPC: "2.0", Result: map[string]any{"ok": true, "deleted": len(unique)}, ID: req.ID}
	default:
		return errResp(req.ID, -32601, "method not found")
	}
}

func errResp(id *int64, code int, msg string) response {
	return response{JSONRPC: "2.0", Error: &rpcError{Code: code, Message: msg}, ID: id}
}
