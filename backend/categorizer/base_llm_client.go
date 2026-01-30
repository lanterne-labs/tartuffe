package categorizer

import (
	"context"
	"digital-mirror/backend/llm"
)

// BaseLLMClient provides common HTTP request logic for LLM API clients.
// This is a wrapper around llm.BaseLLMClient for backward compatibility.
type BaseLLMClient = llm.BaseLLMClient

// NewBaseLLMClient creates a new base client with a default timeout.
func NewBaseLLMClient() *BaseLLMClient {
	return llm.NewBaseLLMClient()
}

// ChatMessage represents a single message in a chat conversation.
// This is a type alias for backward compatibility.
type ChatMessage = llm.ChatMessage

// ResponseFormat controls the response format, e.g., JSON mode.
// This is a type alias for backward compatibility.
type ResponseFormat = llm.ResponseFormat

// ChatRequest is the OpenAI-compatible chat completion request.
// This is a type alias for backward compatibility.
type ChatRequest = llm.ChatRequest

// ChatChoice models a single choice in the completion response.
// This is a type alias for backward compatibility.
type ChatChoice = llm.ChatChoice

// ChatResponse is the OpenAI-compatible completion response envelope.
// This is a type alias for backward compatibility.
type ChatResponse = llm.ChatResponse

// DoRequest is provided for backward compatibility.
// Use llm.BaseLLMClient directly for new code.
func DoRequest(ctx context.Context, client *BaseLLMClient, apiURL, apiKey string, payloadBytes []byte) ([]byte, error) {
	return client.DoRequest(ctx, apiURL, apiKey, payloadBytes)
}
