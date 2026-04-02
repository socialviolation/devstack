package tilt

import (
	"os"
	"regexp"
)

// reLocalResource matches the opening of a local_resource call and captures the service name.
var reLocalResource = regexp.MustCompile(`local_resource\(\s*["']([^"']+)["']`)

// reServeDir captures a serve_dir= value (single or double quoted).
var reServeDir = regexp.MustCompile(`serve_dir\s*=\s*["']([^"']+)["']`)

// ParseTiltfileServeDirs parses a Tiltfile and returns a map of service name → serve_dir.
// Only services that declare a serve_dir are included.
// Errors reading the file are silently ignored (returns empty map).
func ParseTiltfileServeDirs(tiltfilePath string) map[string]string {
	data, err := os.ReadFile(tiltfilePath)
	if err != nil {
		return map[string]string{}
	}

	result := make(map[string]string)

	// Split on local_resource( boundaries so each block is one service.
	// We find each match position for local_resource and extract the block up to the next one.
	content := string(data)
	nameMatches := reLocalResource.FindAllStringIndex(content, -1)
	if len(nameMatches) == 0 {
		return result
	}

	for i, loc := range nameMatches {
		// The block spans from this match to the start of the next (or EOF).
		end := len(content)
		if i+1 < len(nameMatches) {
			end = nameMatches[i+1][0]
		}
		block := content[loc[0]:end]

		// Extract service name
		nameSub := reLocalResource.FindStringSubmatch(block)
		if nameSub == nil {
			continue
		}
		name := nameSub[1]

		// Extract serve_dir
		dirSub := reServeDir.FindStringSubmatch(block)
		if dirSub == nil {
			continue
		}
		result[name] = dirSub[1]
	}

	return result
}
