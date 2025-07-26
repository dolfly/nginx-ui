package upstream

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/0xJacky/Nginx-UI/internal/cache"
	"github.com/0xJacky/Nginx-UI/internal/nginx"
	"github.com/uozi-tech/cosy/logger"
)

// TargetInfo contains proxy target information with source config
type TargetInfo struct {
	ProxyTarget
	ConfigPath string    `json:"config_path"`
	LastSeen   time.Time `json:"last_seen"`
}

// UpstreamService manages upstream availability testing
type UpstreamService struct {
	targets         map[string]*TargetInfo // key: host:port
	availabilityMap map[string]*Status     // key: host:port
	configTargets   map[string][]string    // configPath -> []targetKeys
	globalUpstreams map[string]bool        // global upstream names across all configs
	targetsMutex    sync.RWMutex
	lastUpdateTime  time.Time
	testInProgress  bool
	testMutex       sync.Mutex
}

var (
	upstreamService *UpstreamService
	serviceOnce     sync.Once
)

// GetUpstreamService returns the singleton upstream service instance
func GetUpstreamService() *UpstreamService {
	serviceOnce.Do(func() {
		upstreamService = &UpstreamService{
			targets:         make(map[string]*TargetInfo),
			availabilityMap: make(map[string]*Status),
			configTargets:   make(map[string][]string),
			globalUpstreams: make(map[string]bool),
			lastUpdateTime:  time.Now(),
		}
	})
	return upstreamService
}

// init registers the ParseProxyTargetsFromRawContent callback
func init() {
	cache.RegisterCallback(scanForProxyTargets)
}

// scanForProxyTargets is the callback function for cache scanner
func scanForProxyTargets(configPath string, content []byte) error {
	service := GetUpstreamService()

	// First pass: scan and update global upstream definitions
	service.updateUpstreamDefinitions(configPath, string(content))

	// Second pass: parse proxy targets with updated global upstream context
	targets := ParseProxyTargetsFromRawContent(string(content))
	service.updateTargetsFromConfig(configPath, targets)

	return nil
}

// updateUpstreamDefinitions scans content for upstream definitions and updates global upstream map
func (s *UpstreamService) updateUpstreamDefinitions(configPath string, content string) {
	s.targetsMutex.Lock()
	defer s.targetsMutex.Unlock()

	logger.Debug("updateUpstreamDefinitions: Scanning upstream definitions in", configPath)

	// Use regex to find upstream blocks
	upstreamRegex := regexp.MustCompile(`(?s)upstream\s+([^\s]+)\s*\{`)
	matches := upstreamRegex.FindAllStringSubmatch(content, -1)

	for _, match := range matches {
		if len(match) >= 2 {
			upstreamName := match[1]
			s.globalUpstreams[upstreamName] = true
			logger.Debug("updateUpstreamDefinitions: Added global upstream", upstreamName, "from", configPath)
		}
	}
}

// updateTargetsFromConfig updates proxy targets from a specific config file
func (s *UpstreamService) updateTargetsFromConfig(configPath string, targets []ProxyTarget) {
	s.targetsMutex.Lock()
	defer s.targetsMutex.Unlock()

	now := time.Now()

	// Remove old targets from this config path
	if oldTargetKeys, exists := s.configTargets[configPath]; exists {
		for _, key := range oldTargetKeys {
			if _, exists := s.targets[key]; exists {
				// Only remove if this is the only config using this target
				isOnlyConfig := true
				for otherConfig, otherKeys := range s.configTargets {
					if otherConfig != configPath {
						for _, otherKey := range otherKeys {
							if otherKey == key {
								isOnlyConfig = false
								break
							}
						}
						if !isOnlyConfig {
							break
						}
					}
				}
				if isOnlyConfig {
					delete(s.targets, key)
					delete(s.availabilityMap, key)
					logger.Debug("Removed proxy target:", key, "from config:", configPath)
				} else {
					logger.Debug("Keeping proxy target:", key, "still used by other configs")
				}
			}
		}
	}

	// Add/update new targets
	newTargetKeys := make([]string, 0, len(targets))
	for _, target := range targets {
		key := target.Host + ":" + target.Port
		newTargetKeys = append(newTargetKeys, key)

		if existingTarget, exists := s.targets[key]; exists {
			// Update existing target with latest info
			existingTarget.LastSeen = now
			existingTarget.ConfigPath = configPath // Update to latest config that referenced it
			logger.Debug("Updated proxy target:", key, "from config:", configPath)
		} else {
			// Add new target
			s.targets[key] = &TargetInfo{
				ProxyTarget: target,
				ConfigPath:  configPath,
				LastSeen:    now,
			}
			logger.Debug("Added proxy target:", key, "type:", target.Type, "from config:", configPath)
		}
	}

	// Update config target mapping
	s.configTargets[configPath] = newTargetKeys
	logger.Debug("Config", configPath, "updated with", len(targets), "targets")
}

// GetTargets returns a copy of current proxy targets
func (s *UpstreamService) GetTargets() []ProxyTarget {
	s.targetsMutex.RLock()
	defer s.targetsMutex.RUnlock()

	targets := make([]ProxyTarget, 0, len(s.targets))
	for _, targetInfo := range s.targets {
		targets = append(targets, targetInfo.ProxyTarget)
	}
	return targets
}

// GetTargetInfos returns a copy of current target infos
func (s *UpstreamService) GetTargetInfos() []*TargetInfo {
	s.targetsMutex.RLock()
	defer s.targetsMutex.RUnlock()

	targetInfos := make([]*TargetInfo, 0, len(s.targets))
	for _, targetInfo := range s.targets {
		// Create a copy
		targetInfoCopy := &TargetInfo{
			ProxyTarget: targetInfo.ProxyTarget,
			ConfigPath:  targetInfo.ConfigPath,
			LastSeen:    targetInfo.LastSeen,
		}
		targetInfos = append(targetInfos, targetInfoCopy)
	}
	return targetInfos
}

// GetAvailabilityMap returns a copy of current availability results
func (s *UpstreamService) GetAvailabilityMap() map[string]*Status {
	s.targetsMutex.RLock()
	defer s.targetsMutex.RUnlock()

	result := make(map[string]*Status)
	for k, v := range s.availabilityMap {
		// Create a copy of the status
		result[k] = &Status{
			Online:  v.Online,
			Latency: v.Latency,
		}
	}
	return result
}

// GetGlobalUpstreams returns a copy of the global upstream names map
func (s *UpstreamService) GetGlobalUpstreams() map[string]bool {
	s.targetsMutex.RLock()
	defer s.targetsMutex.RUnlock()

	// Create a copy to avoid race conditions
	result := make(map[string]bool)
	for name, exists := range s.globalUpstreams {
		result[name] = exists
	}
	return result
}

// RefreshGlobalUpstreams rescans all nginx config files to update global upstream definitions
func (s *UpstreamService) RefreshGlobalUpstreams() error {
	s.targetsMutex.Lock()
	defer s.targetsMutex.Unlock()

	// Clear existing global upstreams
	s.globalUpstreams = make(map[string]bool)
	logger.Debug("RefreshGlobalUpstreams: Cleared existing global upstreams")

	// Get nginx config path and scan all config files
	configPath := nginx.GetConfPath()
	logger.Debug("RefreshGlobalUpstreams: Scanning config path:", configPath)

	// Walk through all config files
	err := filepath.WalkDir(configPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip directories and non-regular files
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}

		// Skip files that don't look like nginx config files
		if !strings.HasSuffix(path, ".conf") && !strings.Contains(path, "nginx.conf") {
			return nil
		}

		// Read and scan the file
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			logger.Debug("RefreshGlobalUpstreams: Failed to read file:", path, readErr)
			return nil // Continue with other files
		}

		// Scan for upstream definitions in this file
		s.updateUpstreamDefinitionsFromContent(path, string(content))
		return nil
	})

	if err != nil {
		logger.Error("RefreshGlobalUpstreams: Error walking config directory:", err)
		return err
	}

	logger.Debug("RefreshGlobalUpstreams: Completed scan, found", len(s.globalUpstreams), "global upstreams")
	return nil
}

// updateUpstreamDefinitionsFromContent scans content for upstream definitions (without lock)
func (s *UpstreamService) updateUpstreamDefinitionsFromContent(configPath string, content string) {
	// Use regex to find upstream blocks
	upstreamRegex := regexp.MustCompile(`(?s)upstream\s+([^\s]+)\s*\{`)
	matches := upstreamRegex.FindAllStringSubmatch(content, -1)

	for _, match := range matches {
		if len(match) >= 2 {
			upstreamName := match[1]
			s.globalUpstreams[upstreamName] = true
			logger.Debug("RefreshGlobalUpstreams: Found upstream", upstreamName, "in", configPath)
		}
	}
}

// PerformAvailabilityTest performs availability test for all targets
func (s *UpstreamService) PerformAvailabilityTest() {
	// Prevent concurrent tests
	s.testMutex.Lock()
	if s.testInProgress {
		s.testMutex.Unlock()
		logger.Debug("Availability test already in progress, skipping")
		return
	}
	s.testInProgress = true
	s.testMutex.Unlock()

	// Ensure we reset the flag when done
	defer func() {
		s.testMutex.Lock()
		s.testInProgress = false
		s.testMutex.Unlock()
	}()

	s.targetsMutex.RLock()
	targetCount := len(s.targets)
	s.targetsMutex.RUnlock()

	if targetCount == 0 {
		logger.Debug("No targets to test")
		return
	}

	logger.Debug("Performing availability test for", targetCount, "unique targets")

	// Separate targets into traditional and consul groups from the start
	s.targetsMutex.RLock()
	regularTargetKeys := make([]string, 0, len(s.targets))
	consulTargets := make([]ProxyTarget, 0, len(s.targets))

	for _, targetInfo := range s.targets {
		if targetInfo.ProxyTarget.IsConsul {
			consulTargets = append(consulTargets, targetInfo.ProxyTarget)
		} else {
			// Traditional target - use host:port key format
			key := targetInfo.ProxyTarget.Host + ":" + targetInfo.ProxyTarget.Port
			regularTargetKeys = append(regularTargetKeys, key)
		}
	}
	s.targetsMutex.RUnlock()

	// Initialize results map
	results := make(map[string]*Status)

	// Test traditional targets using the original AvailabilityTest
	if len(regularTargetKeys) > 0 {
		logger.Debug("Testing", len(regularTargetKeys), "traditional targets")
		regularResults := AvailabilityTest(regularTargetKeys)
		for k, v := range regularResults {
			results[k] = v
		}
	}

	// Test consul targets using consul-specific logic
	if len(consulTargets) > 0 {
		logger.Debug("Testing", len(consulTargets), "consul targets")
		consulResults := TestDynamicTargets(consulTargets)
		for k, v := range consulResults {
			results[k] = v
		}
	}

	// Update availability map
	s.targetsMutex.Lock()
	s.availabilityMap = results
	s.targetsMutex.Unlock()

	logger.Debug("Availability test completed for", len(results), "targets")
}

// ClearTargets clears all targets (useful for testing or reloading)
func (s *UpstreamService) ClearTargets() {
	s.targetsMutex.Lock()
	defer s.targetsMutex.Unlock()

	s.targets = make(map[string]*TargetInfo)
	s.availabilityMap = make(map[string]*Status)
	s.configTargets = make(map[string][]string)
	s.lastUpdateTime = time.Now()

	logger.Debug("Cleared all proxy targets")
}

// GetLastUpdateTime returns the last time targets were updated
func (s *UpstreamService) GetLastUpdateTime() time.Time {
	s.targetsMutex.RLock()
	defer s.targetsMutex.RUnlock()
	return s.lastUpdateTime
}

// GetTargetCount returns the number of unique targets
func (s *UpstreamService) GetTargetCount() int {
	s.targetsMutex.RLock()
	defer s.targetsMutex.RUnlock()
	return len(s.targets)
}

// RemoveConfigTargets removes all targets associated with a specific config file
func (s *UpstreamService) RemoveConfigTargets(configPath string) {
	s.targetsMutex.Lock()
	defer s.targetsMutex.Unlock()

	if targetKeys, exists := s.configTargets[configPath]; exists {
		for _, key := range targetKeys {
			// Check if this target is used by other configs
			isUsedByOthers := false
			for otherConfig, otherKeys := range s.configTargets {
				if otherConfig != configPath {
					for _, otherKey := range otherKeys {
						if otherKey == key {
							isUsedByOthers = true
							break
						}
					}
					if isUsedByOthers {
						break
					}
				}
			}

			if !isUsedByOthers {
				delete(s.targets, key)
				delete(s.availabilityMap, key)
				logger.Debug("Removed proxy target:", key, "after config removal:", configPath)
			}
		}
		delete(s.configTargets, configPath)
		s.lastUpdateTime = time.Now()
		logger.Debug("Removed config targets for:", configPath)
	}
}
