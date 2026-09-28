// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build ignore

// 生成随版本打包的 models.dev 快照：go generate ./model/llm（或 make models-dev）。
//
// 只留 Diana 用得到的字段（服务商 API 地址、模型名称、模态、窗口和输出上限），
// 键按字母序、gzip 头不带时间，同样的数据生成的文件逐字节相同，不会凭空出 diff。
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

const source = "https://models.dev/api.json"

type limit struct {
	Context int64 `json:"context,omitempty"`
	Input   int64 `json:"input,omitempty"`
	Output  int64 `json:"output,omitempty"`
}

type modalities struct {
	Input  []string `json:"input,omitempty"`
	Output []string `json:"output,omitempty"`
}

type model struct {
	Name       string     `json:"name,omitempty"`
	Modalities modalities `json:"modalities,omitzero"`
	Limit      limit      `json:"limit,omitzero"`
}

type provider struct {
	API    string           `json:"api,omitempty"`
	Models map[string]model `json:"models"`
}

func main() {
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(source)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("GET %s: %s", source, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		log.Fatal(err)
	}
	var catalog map[string]provider
	if err := json.Unmarshal(body, &catalog); err != nil {
		log.Fatal(err)
	}
	if len(catalog) == 0 {
		log.Fatal("models.dev returned an empty catalog")
	}
	trimmed, err := json.Marshal(catalog)
	if err != nil {
		log.Fatal(err)
	}
	var out bytes.Buffer
	writer, err := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := writer.Write(trimmed); err != nil {
		log.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("models_dev_snapshot.json.gz", out.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	models := 0
	for _, item := range catalog {
		models += len(item.Models)
	}
	fmt.Printf("models.dev snapshot: %d providers, %d models, %d bytes\n", len(catalog), models, out.Len())
}
