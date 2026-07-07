package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"kiro-go/config"
	"net/http"
	"os"
	"strings"
	"time"
)

const defaultResponsesModel = "claude-sonnet-4.5"

func (h *Handler) handleOpenAIResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.sendOpenAIError(w, 400, "invalid_request_error", "Failed to read request body")
		return
	}

	var req ResponsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		h.sendOpenAIError(w, 400, "invalid_request_error", "Invalid JSON")
		return
	}

	if strings.TrimSpace(req.Model) == "" {
		req.Model = defaultResponsesModel
	}

	storedInputCopy := append(json.RawMessage(nil), req.Input...)

	storeResponse := true
	if req.Store != nil {
		storeResponse = *req.Store
	}

	var historyMessages []OpenAIMessage
	if req.PreviousResponseID != "" {
		prev, loadErr := loadResponse(req.PreviousResponseID)
		if loadErr != nil {
			h.sendOpenAIError(w, 404, "invalid_request_error",
				fmt.Sprintf("previous_response_id not found: %v", loadErr))
			return
		}
		historyMessages = expandPreviousResponseHistory(prev)
	}

	inputMessages, err := parseResponsesInput(req.Input)
	if err != nil {
		h.sendOpenAIError(w, 400, "invalid_request_error", err.Error())
		return
	}

	finalMessages := make([]OpenAIMessage, 0, len(historyMessages)+len(inputMessages)+1)
	finalMessages = append(finalMessages, historyMessages...)
	if strings.TrimSpace(req.Instructions) != "" {
		// New instructions on this turn always take effect, even when
		// continuing from previous_response_id. Place them after the
		// expanded history so they apply to the current and future turns,
		// while ancestor instructions (re-emitted by expandPreviousResponseHistory)
		// stay in scope for the historical exchanges they shaped.
		finalMessages = append(finalMessages, OpenAIMessage{
			Role:    "system",
			Content: req.Instructions,
		})
	}
	finalMessages = append(finalMessages, inputMessages...)

	if len(finalMessages) == 0 {
		h.sendOpenAIError(w, 400, "invalid_request_error", "input must contain at least one message")
		return
	}

	hasUser := false
	for _, m := range finalMessages {
		if m.Role == "user" {
			hasUser = true
			break
		}
	}
	if !hasUser {
		h.sendOpenAIError(w, 400, "invalid_request_error", "input must contain at least one user message")
		return
	}

	openaiReq := buildOpenAIRequestFromResponses(&req, finalMessages)

	thinkingCfg := config.GetThinkingConfig()
	actualModel, thinking, agentic, chatOnly := ParseModelThinkingAndAgentic(openaiReq.Model, thinkingCfg.Suffix)
	thinking = enrichOpenAIThinking(thinking, nil, nil, openaiReq)
	openaiReq.Model = actualModel

	estimatedInputTokens := estimateOpenAIRequestInputTokens(openaiReq)
	kiroPayload := OpenAIToKiro(openaiReq, thinking, agentic, chatOnly)

	apiKeyID := apiKeyIDFromContext(r.Context())
	respID := generateResponseID()

	if req.Stream {
		h.handleResponsesStream(w, kiroPayload, actualModel, thinking, estimatedInputTokens,
			apiKeyID, respID, &req, storedInputCopy, storeResponse)
		return
	}

	h.handleResponsesNonStream(w, kiroPayload, actualModel, thinking, estimatedInputTokens,
		apiKeyID, respID, &req, storedInputCopy, storeResponse)
}

func (h *Handler) handleResponsesNonStream(
	w http.ResponseWriter, payload *KiroPayload, model string, thinking bool,
	estimatedInputTokens int, apiKeyID, respID string,
	req *ResponsesRequest, storedInput json.RawMessage, storeResponse bool,
) {
	excluded := make(map[string]bool)
	var lastErr error
	reqStart := time.Now()

	for attempt := 0; attempt < maxAccountRetryAttempts; attempt++ {
		account := h.pool.GetNextForModelExcluding(model, excluded)
		if account == nil {
			break
		}
		if err := h.ensureValidToken(account); err != nil {
			lastErr = err
			excluded[account.ID] = true
			h.handleAccountFailure(account, err)
			continue
		}

		var content, reasoningContent string
		var toolUses []KiroToolUse
		var inputTokens, outputTokens int
		var credits float64
		var realInputTokens int

		callback := &KiroStreamCallback{
			OnText: func(text string, isThinking bool) {
				if isThinking {
					reasoningContent += text
				} else {
					content += text
				}
			},
			OnToolUse:  func(tu KiroToolUse) { toolUses = append(toolUses, tu) },
			OnComplete: func(inTok, outTok int) { inputTokens = inTok; outputTokens = outTok },
			OnCredits:  func(c float64) { credits = c },
			OnContextUsage: func(pct float64) {
				realInputTokens = int(pct * float64(getContextWindowSize(model)) / 100.0)
			},
		}

		err := CallKiroAPI(account, payload, callback)
		if err != nil {
			lastErr = err
			excluded[account.ID] = true
			h.handleAccountFailure(account, err)
			continue
		}

		finalContent, _ := extractThinkingFromContent(content)
		if !thinking {
			reasoningContent = ""
		}

		if realInputTokens > 0 {
			inputTokens = realInputTokens
		} else if inputTokens <= 0 {
			inputTokens = estimatedInputTokens
		}
		outputTokens = estimateOpenAIOutputTokens(finalContent, reasoningContent, toolUses)

		h.recordSuccessForApiKey(apiKeyID, inputTokens, outputTokens, credits)
		h.pool.RecordSuccess(account.ID)
		h.pool.UpdateStats(account.ID, inputTokens+outputTokens, credits)
		h.recordSuccessLog("responses", model, account.ID, inputTokens+outputTokens, credits, time.Since(reqStart).Milliseconds())

		respObj := buildResponsesObject(respID, model, finalContent, toolUses, inputTokens, outputTokens, req)
		respObj.StoredInput = storedInput
		respObj.Instructions = req.Instructions

		if storeResponse {
			if saveErr := saveResponse(respObj); saveErr != nil {
				logResponsesPersistFailure(respObj.ID, saveErr)
			}
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(respObj)
		return
	}

	if lastErr == nil {
		h.sendOpenAIError(w, 503, "server_error", "No available accounts")
		return
	}
	h.recordFailureWithDetails("responses", model, "", lastErr)
	h.sendOpenAIErrorFromUpstream(w, lastErr)
}

func (h *Handler) handleGetResponse(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	resp, err := loadResponse(id)
	if err != nil {
		if os.IsNotExist(err) {
			h.sendOpenAIError(w, http.StatusNotFound, "invalid_request_error",
				fmt.Sprintf("Response with id '%s' not found.", id))
			return
		}
		h.sendOpenAIError(w, http.StatusNotFound, "invalid_request_error", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp)
}

func buildResponsesObject(
	id, model, content string, toolUses []KiroToolUse,
	inputTokens, outputTokens int, req *ResponsesRequest,
) *ResponsesObject {
	output := make([]ResponseOutputItem, 0, 1+len(toolUses))

	if strings.TrimSpace(content) != "" {
		output = append(output, ResponseOutputItem{
			ID:     generateOutputItemID("msg"),
			Type:   "message",
			Role:   "assistant",
			Status: "completed",
			Content: []ResponseContentPart{{
				Type: "output_text",
				Text: content,
			}},
		})
	}

	for _, tu := range toolUses {
		output = append(output, ResponseOutputItem{
			ID:        generateOutputItemID("fc"),
			Type:      "function_call",
			Status:    "completed",
			CallID:    tu.ToolUseID,
			Name:      tu.Name,
			Arguments: MarshalToolUseArguments(tu),
		})
	}

	if len(output) == 0 {
		output = append(output, ResponseOutputItem{
			ID:     generateOutputItemID("msg"),
			Type:   "message",
			Role:   "assistant",
			Status: "completed",
			Content: []ResponseContentPart{{
				Type: "output_text",
				Text: "",
			}},
		})
	}

	return &ResponsesObject{
		ID:                 id,
		Object:             "response",
		CreatedAt:          time.Now().Unix(),
		Status:             "completed",
		Model:              model,
		Output:             output,
		Usage:              ResponsesUsage{InputTokens: inputTokens, OutputTokens: outputTokens, TotalTokens: inputTokens + outputTokens},
		PreviousResponseID: req.PreviousResponseID,
		Metadata:           req.Metadata,
	}
}

func (h *Handler) handleResponsesStream(
	w http.ResponseWriter, payload *KiroPayload, model string, thinking bool,
	estimatedInputTokens int, apiKeyID, respID string,
	req *ResponsesRequest, storedInput json.RawMessage, storeResponse bool,
) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		h.sendOpenAIError(w, 500, "server_error", "Streaming not supported")
		return
	}

	send := func(eventName string, payload interface{}) {
		data, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, string(data))
		flusher.Flush()
	}

	createdAt := time.Now().Unix()
	initial := &ResponsesObject{
		ID:                 respID,
		Object:             "response",
		CreatedAt:          createdAt,
		Status:             "in_progress",
		Model:              model,
		Output:             []ResponseOutputItem{},
		Usage:              ResponsesUsage{},
		PreviousResponseID: req.PreviousResponseID,
		Metadata:           req.Metadata,
	}
	send("response.created", map[string]interface{}{
		"type":     "response.created",
		"response": initial,
	})

	excluded := make(map[string]bool)
	var lastErr error
	responseStarted := false
	reqStart := time.Now()

	for attempt := 0; attempt < maxAccountRetryAttempts; attempt++ {
		account := h.pool.GetNextForModelExcluding(model, excluded)
		if account == nil {
			break
		}
		if err := h.ensureValidToken(account); err != nil {
			lastErr = err
			excluded[account.ID] = true
			h.handleAccountFailure(account, err)
			continue
		}

		send("response.in_progress", map[string]interface{}{
			"type":     "response.in_progress",
			"response": initial,
		})

		var (
			fullText             strings.Builder
			reasoningText        strings.Builder
			toolUses             []KiroToolUse
			inputTokens          int
			outputTokens         int
			credits              float64
			realInputTokens      int
			toolNarrationFilter  ToolNarrationStreamFilter
		)

		messageItemID := generateOutputItemID("msg")
		messageStarted := false
		outputIndex := 0
		contentIndex := 0
		fcIDByToolUseID := make(map[string]string)
		toolOutputIndexByID := make(map[string]int)
		toolStreamStarted := make(map[string]bool)

		finalizeMessageIfStarted := func() {
			if !messageStarted {
				return
			}
			send("response.content_part.done", map[string]interface{}{
				"type":          "response.content_part.done",
				"item_id":       messageItemID,
				"output_index":  outputIndex,
				"content_index": contentIndex,
				"part": map[string]interface{}{
					"type": "output_text",
					"text": fullText.String(),
				},
			})
			send("response.output_item.done", map[string]interface{}{
				"type":         "response.output_item.done",
				"output_index": outputIndex,
				"item": map[string]interface{}{
					"id":     messageItemID,
					"type":   "message",
					"role":   "assistant",
					"status": "completed",
					"content": []map[string]interface{}{{
						"type": "output_text",
						"text": fullText.String(),
					}},
				},
			})
			messageStarted = false
			outputIndex++
		}

		emitFunctionCallDone := func(tu KiroToolUse, fcID string, idx int, args string) {
			send("response.output_item.done", map[string]interface{}{
				"type":         "response.output_item.done",
				"output_index": idx,
				"item": map[string]interface{}{
					"id":        fcID,
					"type":      "function_call",
					"status":    "completed",
					"call_id":   tu.ToolUseID,
					"name":      tu.Name,
					"arguments": string(args),
				},
			})
		}

		ensureMessageStarted := func() {
			if messageStarted {
				return
			}
			messageStarted = true
			send("response.output_item.added", map[string]interface{}{
				"type":         "response.output_item.added",
				"output_index": outputIndex,
				"item": map[string]interface{}{
					"id":      messageItemID,
					"type":    "message",
					"role":    "assistant",
					"status":  "in_progress",
					"content": []map[string]interface{}{},
				},
			})
			send("response.content_part.added", map[string]interface{}{
				"type":          "response.content_part.added",
				"item_id":       messageItemID,
				"output_index":  outputIndex,
				"content_index": contentIndex,
				"part": map[string]interface{}{
					"type": "output_text",
					"text": "",
				},
			})
		}

		callback := &KiroStreamCallback{
			OnText: func(text string, isThinking bool) {
				if text == "" {
					return
				}
				if isThinking {
					reasoningText.WriteString(text)
					return
				}
				fullText.WriteString(text)
				filtered, embedded := toolNarrationFilter.Process(text)
				for _, tu := range embedded {
					toolUses = append(toolUses, tu)
					if toolStreamStarted[tu.ToolUseID] {
						continue
					}
					finalizeMessageIfStarted()
					fcID := generateOutputItemID("fc")
					idx := outputIndex
					args := MarshalToolUseArguments(tu)
					toolStreamStarted[tu.ToolUseID] = true
					send("response.output_item.added", map[string]interface{}{
						"type":         "response.output_item.added",
						"output_index": idx,
						"item": map[string]interface{}{
							"id":        fcID,
							"type":      "function_call",
							"status":    "in_progress",
							"call_id":   tu.ToolUseID,
							"name":      tu.Name,
							"arguments": "",
						},
					})
					send("response.function_call_arguments.delta", map[string]interface{}{
						"type":         "response.function_call_arguments.delta",
						"item_id":      fcID,
						"output_index": idx,
						"delta":        args,
					})
					emitFunctionCallDone(tu, fcID, idx, args)
					outputIndex++
					responseStarted = true
				}
				if filtered == "" {
					return
				}
				ensureMessageStarted()
				send("response.output_text.delta", map[string]interface{}{
					"type":          "response.output_text.delta",
					"item_id":       messageItemID,
					"output_index":  outputIndex,
					"content_index": contentIndex,
					"delta":         filtered,
				})
				responseStarted = true
			},
			OnToolUseStart: func(tu KiroToolUse) {
				finalizeMessageIfStarted()
				fcID := generateOutputItemID("fc")
				fcIDByToolUseID[tu.ToolUseID] = fcID
				toolOutputIndexByID[tu.ToolUseID] = outputIndex
				toolStreamStarted[tu.ToolUseID] = true
				send("response.output_item.added", map[string]interface{}{
					"type":         "response.output_item.added",
					"output_index": outputIndex,
					"item": map[string]interface{}{
						"id":        fcID,
						"type":      "function_call",
						"status":    "in_progress",
						"call_id":   tu.ToolUseID,
						"name":      tu.Name,
						"arguments": "",
					},
				})
				responseStarted = true
			},
			OnToolUseInputDelta: func(toolUseID, partialJSON string) {
				if partialJSON == "" {
					return
				}
				fcID, ok := fcIDByToolUseID[toolUseID]
				idx, okIdx := toolOutputIndexByID[toolUseID]
				if !ok || !okIdx {
					return
				}
				send("response.function_call_arguments.delta", map[string]interface{}{
					"type":         "response.function_call_arguments.delta",
					"item_id":      fcID,
					"output_index": idx,
					"delta":        partialJSON,
				})
				responseStarted = true
			},
			OnToolUse: func(tu KiroToolUse) {
				args := MarshalToolUseArguments(tu)
				toolUses = append(toolUses, tu)

				fcID, streamed := fcIDByToolUseID[tu.ToolUseID]
				idx := toolOutputIndexByID[tu.ToolUseID]
				if !streamed {
					finalizeMessageIfStarted()
					fcID = generateOutputItemID("fc")
					idx = outputIndex
					send("response.output_item.added", map[string]interface{}{
						"type":         "response.output_item.added",
						"output_index": idx,
						"item": map[string]interface{}{
							"id":        fcID,
							"type":      "function_call",
							"status":    "in_progress",
							"call_id":   tu.ToolUseID,
							"name":      tu.Name,
							"arguments": "",
						},
					})
					send("response.function_call_arguments.delta", map[string]interface{}{
						"type":         "response.function_call_arguments.delta",
						"item_id":      fcID,
						"output_index": idx,
						"delta":        string(args),
					})
				}
				emitFunctionCallDone(tu, fcID, idx, args)
				delete(fcIDByToolUseID, tu.ToolUseID)
				delete(toolOutputIndexByID, tu.ToolUseID)
				delete(toolStreamStarted, tu.ToolUseID)
				outputIndex++
				responseStarted = true
			},
			OnComplete: func(inTok, outTok int) { inputTokens = inTok; outputTokens = outTok },
			OnCredits:  func(c float64) { credits = c },
			OnContextUsage: func(pct float64) {
				realInputTokens = int(pct * float64(getContextWindowSize(model)) / 100.0)
			},
		}

		err := CallKiroAPI(account, payload, callback)
		if err != nil {
			if !responseStarted {
				lastErr = err
				excluded[account.ID] = true
				h.handleAccountFailure(account, err)
				continue
			}
			_, errType, msg := upstreamClientError(err, "responses")
			send("response.failed", map[string]interface{}{
				"type": "response.failed",
				"response": map[string]interface{}{
					"id":     respID,
					"status": "failed",
					"error": map[string]string{
						"type":    errType,
						"message": msg,
					},
				},
			})
			h.recordFailureWithDetails("responses", model, account.ID, err)
			return
		}

		rawContent := fullText.String()
		if tail := toolNarrationFilter.Flush(); tail != "" {
			rawContent += tail
		}
		finalContent, _ := extractThinkingFromContent(rawContent)
		finalContent, toolUses = mergeEmbeddedToolUses(finalContent, toolUses)
		for _, tu := range toolUses {
			if toolStreamStarted[tu.ToolUseID] {
				continue
			}
			args := MarshalToolUseArguments(tu)
			finalizeMessageIfStarted()
			fcID := generateOutputItemID("fc")
			idx := outputIndex
			send("response.output_item.added", map[string]interface{}{
				"type":         "response.output_item.added",
				"output_index": idx,
				"item": map[string]interface{}{
					"id":        fcID,
					"type":      "function_call",
					"status":    "in_progress",
					"call_id":   tu.ToolUseID,
					"name":      tu.Name,
					"arguments": "",
				},
			})
			send("response.function_call_arguments.delta", map[string]interface{}{
				"type":         "response.function_call_arguments.delta",
				"item_id":      fcID,
				"output_index": idx,
				"delta":        string(args),
			})
			emitFunctionCallDone(tu, fcID, idx, args)
			outputIndex++
			toolStreamStarted[tu.ToolUseID] = true
		}
		reasoning := reasoningText.String()
		if !thinking {
			reasoning = ""
		}

		if messageStarted {
			send("response.content_part.done", map[string]interface{}{
				"type":          "response.content_part.done",
				"item_id":       messageItemID,
				"output_index":  outputIndex,
				"content_index": contentIndex,
				"part": map[string]interface{}{
					"type": "output_text",
					"text": finalContent,
				},
			})
			send("response.output_item.done", map[string]interface{}{
				"type":         "response.output_item.done",
				"output_index": outputIndex,
				"item": map[string]interface{}{
					"id":     messageItemID,
					"type":   "message",
					"role":   "assistant",
					"status": "completed",
					"content": []map[string]interface{}{{
						"type": "output_text",
						"text": finalContent,
					}},
				},
			})
		}

		if realInputTokens > 0 {
			inputTokens = realInputTokens
		} else if inputTokens <= 0 {
			inputTokens = estimatedInputTokens
		}
		outputTokens = estimateOpenAIOutputTokens(finalContent, reasoning, toolUses)

		h.recordSuccessForApiKey(apiKeyID, inputTokens, outputTokens, credits)
		h.pool.RecordSuccess(account.ID)
		h.pool.UpdateStats(account.ID, inputTokens+outputTokens, credits)
		h.recordSuccessLog("responses", model, account.ID, inputTokens+outputTokens, credits, time.Since(reqStart).Milliseconds())

		respObj := buildResponsesObject(respID, model, finalContent, toolUses, inputTokens, outputTokens, req)
		respObj.CreatedAt = createdAt
		respObj.StoredInput = storedInput
		respObj.Instructions = req.Instructions

		if storeResponse {
			if saveErr := saveResponse(respObj); saveErr != nil {
				logResponsesPersistFailure(respObj.ID, saveErr)
			}
		}

		send("response.completed", map[string]interface{}{
			"type":     "response.completed",
			"response": respObj,
		})
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}

	if lastErr == nil {
		send("response.failed", map[string]interface{}{
			"type": "response.failed",
			"response": map[string]interface{}{
				"id":     respID,
				"status": "failed",
				"error": map[string]string{
					"type":    "server_error",
					"message": "No available accounts",
				},
			},
		})
		return
	}
	h.recordFailureWithDetails("responses", model, "", lastErr)
	_, errType, msg := upstreamClientError(lastErr, "responses")
	send("response.failed", map[string]interface{}{
		"type": "response.failed",
		"response": map[string]interface{}{
			"id":     respID,
			"status": "failed",
			"error": map[string]string{
				"type":    errType,
				"message": msg,
			},
		},
	})
}

func buildOpenAIRequestFromResponses(req *ResponsesRequest, messages []OpenAIMessage) *OpenAIRequest {
	out := &OpenAIRequest{
		Model:    req.Model,
		Messages: messages,
		Stream:   req.Stream,
		Tools:    req.Tools,
	}
	if req.Temperature != nil {
		out.Temperature = *req.Temperature
	}
	if req.TopP != nil {
		out.TopP = *req.TopP
	}
	if req.MaxOutputTokens != nil {
		out.MaxTokens = *req.MaxOutputTokens
	}
	if len(req.ToolChoice) > 0 && string(req.ToolChoice) != "null" {
		var toolChoice interface{}
		if err := json.Unmarshal(req.ToolChoice, &toolChoice); err == nil {
			out.ToolChoice = toolChoice
		}
	}
	if len(req.Reasoning) > 0 {
		var reasoning map[string]interface{}
		if err := json.Unmarshal(req.Reasoning, &reasoning); err == nil {
			if effort, ok := reasoning["effort"].(string); ok {
				out.ReasoningEffort = effort
			}
		}
	}
	if len(req.Text) > 0 {
		var textObj map[string]interface{}
		if err := json.Unmarshal(req.Text, &textObj); err == nil {
			if format, ok := textObj["format"].(map[string]interface{}); ok {
				out.ResponseFormat = format
			}
		}
	}
	out.ForceSystemInject = strings.TrimSpace(req.Instructions) != ""
	return out
}
