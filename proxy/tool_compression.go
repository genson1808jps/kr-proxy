package proxy

import (
	"encoding/json"
	"unicode/utf8"

	"kiro-go/logger"
)

const (
	ToolCompressionTargetSize = 20 * 1024
	MinToolDescriptionLength  = 50
)

func calculateToolsSize(tools []KiroToolWrapper) int {
	if len(tools) == 0 {
		return 0
	}
	data, err := json.Marshal(tools)
	if err != nil {
		logger.Warnf("[ToolCompression] failed to marshal tools for size calculation: %v", err)
		return 0
	}
	return len(data)
}

func simplifyInputSchema(schema interface{}) interface{} {
	if schema == nil {
		return nil
	}

	schemaMap, ok := schema.(map[string]interface{})
	if !ok {
		return schema
	}

	simplified := make(map[string]interface{})

	if t, ok := schemaMap["type"]; ok {
		simplified["type"] = t
	}
	if enum, ok := schemaMap["enum"]; ok {
		simplified["enum"] = enum
	}
	if required, ok := schemaMap["required"]; ok {
		simplified["required"] = required
	}

	if properties, ok := schemaMap["properties"].(map[string]interface{}); ok {
		simplifiedProps := make(map[string]interface{})
		for key, value := range properties {
			simplifiedProps[key] = simplifyInputSchema(value)
		}
		simplified["properties"] = simplifiedProps
	}

	if items, ok := schemaMap["items"]; ok {
		simplified["items"] = simplifyInputSchema(items)
	}

	if additionalProps, ok := schemaMap["additionalProperties"]; ok {
		simplified["additionalProperties"] = simplifyInputSchema(additionalProps)
	}

	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := schemaMap[key].([]interface{}); ok {
			simplifiedArr := make([]interface{}, len(arr))
			for i, item := range arr {
				simplifiedArr[i] = simplifyInputSchema(item)
			}
			simplified[key] = simplifiedArr
		}
	}

	return simplified
}

func compressToolDescription(description string, targetLength int) string {
	if targetLength < MinToolDescriptionLength {
		targetLength = MinToolDescriptionLength
	}

	if len(description) <= targetLength {
		return description
	}

	truncLen := targetLength - 3
	for truncLen > 0 && !utf8.RuneStart(description[truncLen]) {
		truncLen--
	}

	if truncLen <= 0 {
		return description[:MinToolDescriptionLength]
	}

	return description[:truncLen] + "..."
}

func compressToolsIfNeeded(tools []KiroToolWrapper) []KiroToolWrapper {
	if len(tools) == 0 {
		return tools
	}

	originalSize := calculateToolsSize(tools)
	if originalSize <= ToolCompressionTargetSize {
		return tools
	}

	logger.Infof("[ToolCompression] tools size %d bytes exceeds target %d bytes, compressing",
		originalSize, ToolCompressionTargetSize)

	compressedTools := make([]KiroToolWrapper, len(tools))
	copy(compressedTools, tools)

	for i := range compressedTools {
		compressedTools[i].ToolSpecification.InputSchema.JSON =
			simplifyInputSchema(compressedTools[i].ToolSpecification.InputSchema.JSON)
	}

	sizeAfterSchema := calculateToolsSize(compressedTools)
	if sizeAfterSchema <= ToolCompressionTargetSize {
		logger.Infof("[ToolCompression] compression complete after schema simplification, final size: %d bytes", sizeAfterSchema)
		return compressedTools
	}

	sizeToReduce := float64(sizeAfterSchema - ToolCompressionTargetSize)
	var totalDescLen float64
	for _, tool := range compressedTools {
		totalDescLen += float64(len(tool.ToolSpecification.Description))
	}

	if totalDescLen > 0 {
		keepRatio := 1.0 - (sizeToReduce / totalDescLen)
		if keepRatio > 1.0 {
			keepRatio = 1.0
		} else if keepRatio < 0 {
			keepRatio = 0
		}

		for i := range compressedTools {
			desc := compressedTools[i].ToolSpecification.Description
			targetLen := int(float64(len(desc)) * keepRatio)
			compressedTools[i].ToolSpecification.Description = compressToolDescription(desc, targetLen)
		}
	}

	finalSize := calculateToolsSize(compressedTools)
	logger.Infof("[ToolCompression] compression complete, original: %d bytes, final: %d bytes (%.1f%% reduction)",
		originalSize, finalSize, float64(originalSize-finalSize)/float64(originalSize)*100)

	return compressedTools
}
