package upstream

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/0xJacky/Nginx-UI/settings"
	"github.com/uozi-tech/cosy/logger"
)

// ProxyTarget represents a proxy destination
type ProxyTarget struct {
	Host       string `json:"host"`
	Port       string `json:"port"`
	Type       string `json:"type"`        // "proxy_pass", "grpc_pass" or "upstream"
	Resolver   string `json:"resolver"`    // DNS resolver address (e.g., "127.0.0.1:8600")
	IsConsul   bool   `json:"is_consul"`   // Whether this is a consul service discovery target
	ServiceURL string `json:"service_url"` // Full service URL for consul (e.g., "service.consul service=redacted-net resolve")
}

// UpstreamContext contains upstream-level configuration
type UpstreamContext struct {
	Name     string
	Resolver string
}

// ParseProxyTargetsFromMultipleConfigs parses proxy targets from multiple nginx configuration files
// This function can properly handle upstream references across different configuration files
func ParseProxyTargetsFromMultipleConfigs(configContents map[string]string) []ProxyTarget {
	var allTargets []ProxyTarget
	logger.Debug("ParseProxyTargetsFromMultipleConfigs: Starting to parse", len(configContents), "configuration files")

	// First pass: collect all upstream definitions from all configs
	globalUpstreamNames := make(map[string]bool)
	globalUpstreamContexts := make(map[string]*UpstreamContext)

	// Scan all configs for upstream definitions
	for configPath, content := range configContents {
		logger.Debug("ParseProxyTargetsFromMultipleConfigs: Scanning upstream blocks in", configPath)
		upstreamRegex := regexp.MustCompile(`(?s)upstream\s+([^\s]+)\s*\{([^}]+)\}`)
		upstreamMatches := upstreamRegex.FindAllStringSubmatch(content, -1)

		for _, match := range upstreamMatches {
			if len(match) >= 3 {
				upstreamName := match[1]
				globalUpstreamNames[upstreamName] = true
				upstreamContent := match[2]
				logger.Debug("ParseProxyTargetsFromMultipleConfigs: Found upstream", upstreamName, "in", configPath)

				// Create upstream context
				ctx := &UpstreamContext{
					Name: upstreamName,
				}

				// Extract resolver information
				resolverRegex := regexp.MustCompile(`(?m)^\s*resolver\s+([^;]+);`)
				if resolverMatch := resolverRegex.FindStringSubmatch(upstreamContent); len(resolverMatch) >= 2 {
					resolverParts := strings.Fields(resolverMatch[1])
					if len(resolverParts) > 0 {
						ctx.Resolver = resolverParts[0]
						logger.Debug("ParseProxyTargetsFromMultipleConfigs: Found resolver for upstream", upstreamName+":", ctx.Resolver)
					}
				}

				globalUpstreamContexts[upstreamName] = ctx

				// Parse servers in this upstream
				serverRegex := regexp.MustCompile(`(?m)^\s*server\s+([^;]+);`)
				serverMatches := serverRegex.FindAllStringSubmatch(upstreamContent, -1)
				logger.Debug("ParseProxyTargetsFromMultipleConfigs: Found", len(serverMatches), "servers in upstream", upstreamName)

				for _, serverMatch := range serverMatches {
					if len(serverMatch) >= 2 {
						target := parseServerAddress(strings.TrimSpace(serverMatch[1]), "upstream", ctx)
						if target.Host != "" {
							allTargets = append(allTargets, target)
							logger.Debug("ParseProxyTargetsFromMultipleConfigs: Added upstream target:", target.Host+":"+target.Port, "from", configPath)
						}
					}
				}
			}
		}
	}

	// Second pass: parse proxy_pass and grpc_pass directives, now with global upstream knowledge
	for configPath, content := range configContents {
		logger.Debug("ParseProxyTargetsFromMultipleConfigs: Parsing proxy directives in", configPath)

		// Parse proxy_pass directives
		proxyPassRegex := regexp.MustCompile(`(?m)^\s*proxy_pass\s+([^;]+);`)
		proxyMatches := proxyPassRegex.FindAllStringSubmatch(content, -1)
		logger.Debug("ParseProxyTargetsFromMultipleConfigs: Found", len(proxyMatches), "proxy_pass directives in", configPath)

		for _, match := range proxyMatches {
			if len(match) >= 2 {
				proxyPassURL := strings.TrimSpace(match[1])
				logger.Debug("ParseProxyTargetsFromMultipleConfigs: Processing proxy_pass:", proxyPassURL, "from", configPath)

				// Check against global upstream names
				if !isUpstreamReference(proxyPassURL, globalUpstreamNames) {
					target := parseProxyPassURL(proxyPassURL, "proxy_pass")
					if target.Host != "" {
						allTargets = append(allTargets, target)
						logger.Debug("ParseProxyTargetsFromMultipleConfigs: Added proxy_pass target:", target.Host+":"+target.Port, "from", configPath)
					}
				} else {
					logger.Debug("ParseProxyTargetsFromMultipleConfigs: Skipping proxy_pass upstream reference:", proxyPassURL, "from", configPath)
				}
			}
		}

		// Parse grpc_pass directives
		grpcPassRegex := regexp.MustCompile(`(?m)^\s*grpc_pass\s+([^;]+);`)
		grpcMatches := grpcPassRegex.FindAllStringSubmatch(content, -1)
		logger.Debug("ParseProxyTargetsFromMultipleConfigs: Found", len(grpcMatches), "grpc_pass directives in", configPath)

		for _, match := range grpcMatches {
			if len(match) >= 2 {
				grpcPassURL := strings.TrimSpace(match[1])
				logger.Debug("ParseProxyTargetsFromMultipleConfigs: Processing grpc_pass:", grpcPassURL, "from", configPath)

				// Check against global upstream names
				if !isUpstreamReference(grpcPassURL, globalUpstreamNames) {
					target := parseProxyPassURL(grpcPassURL, "grpc_pass")
					if target.Host != "" {
						allTargets = append(allTargets, target)
						logger.Debug("ParseProxyTargetsFromMultipleConfigs: Added grpc_pass target:", target.Host+":"+target.Port, "from", configPath)
					}
				} else {
					logger.Debug("ParseProxyTargetsFromMultipleConfigs: Skipping grpc_pass upstream reference:", grpcPassURL, "from", configPath)
				}
			}
		}
	}

	result := deduplicateTargets(allTargets)
	logger.Debug("ParseProxyTargetsFromMultipleConfigs: Final result - found", len(result), "unique targets from", len(configContents), "config files")
	for i, target := range result {
		logger.Debugf("ParseProxyTargetsFromMultipleConfigs: Target %d: %s:%s (%s)", i+1, target.Host, target.Port, target.Type)
	}

	return result
}

// ParseProxyTargetsFromRawContent parses proxy targets from raw nginx configuration content
func ParseProxyTargetsFromRawContent(content string) []ProxyTarget {
	var targets []ProxyTarget
	logger.Debug("ParseProxyTargetsFromRawContent: Starting to parse content, length:", len(content))

	// First, collect all upstream names and their contexts (local to this file)
	localUpstreamNames := make(map[string]bool)
	upstreamContexts := make(map[string]*UpstreamContext)

	// Get global upstream names from the service (from all config files)
	globalUpstreamNames := make(map[string]bool)
	if service := GetUpstreamService(); service != nil {
		globalUpstreamNames = service.GetGlobalUpstreams()
		logger.Debug("ParseProxyTargetsFromRawContent: Loaded", len(globalUpstreamNames), "global upstream names")
	}

	// Combine local and global upstream names
	allUpstreamNames := make(map[string]bool)
	for name := range globalUpstreamNames {
		allUpstreamNames[name] = true
	}
	upstreamRegex := regexp.MustCompile(`(?s)upstream\s+([^\s]+)\s*\{([^}]+)\}`)
	upstreamMatches := upstreamRegex.FindAllStringSubmatch(content, -1)
	logger.Debug("ParseProxyTargetsFromRawContent: Found", len(upstreamMatches), "upstream blocks")

	// Parse upstream blocks and collect upstream names
	for _, match := range upstreamMatches {
		if len(match) >= 3 {
			upstreamName := match[1]
			localUpstreamNames[upstreamName] = true
			allUpstreamNames[upstreamName] = true
			upstreamContent := match[2]
			logger.Debug("ParseProxyTargetsFromRawContent: Processing upstream:", upstreamName)

			// Create upstream context
			ctx := &UpstreamContext{
				Name: upstreamName,
			}

			// Extract resolver information from upstream block
			resolverRegex := regexp.MustCompile(`(?m)^\s*resolver\s+([^;]+);`)
			if resolverMatch := resolverRegex.FindStringSubmatch(upstreamContent); len(resolverMatch) >= 2 {
				// Parse resolver directive (e.g., "127.0.0.1:8600 valid=5s ipv6=off")
				resolverParts := strings.Fields(resolverMatch[1])
				if len(resolverParts) > 0 {
					ctx.Resolver = resolverParts[0] // Take the first part as resolver address
					logger.Debug("ParseProxyTargetsFromRawContent: Found resolver for upstream", upstreamName+":", ctx.Resolver)
				}
			}

			upstreamContexts[upstreamName] = ctx

			serverRegex := regexp.MustCompile(`(?m)^\s*server\s+([^;]+);`)
			serverMatches := serverRegex.FindAllStringSubmatch(upstreamContent, -1)
			logger.Debug("ParseProxyTargetsFromRawContent: Found", len(serverMatches), "servers in upstream", upstreamName)

			for _, serverMatch := range serverMatches {
				if len(serverMatch) >= 2 {
					target := parseServerAddress(strings.TrimSpace(serverMatch[1]), "upstream", ctx)
					if target.Host != "" {
						targets = append(targets, target)
						logger.Debug("ParseProxyTargetsFromRawContent: Added upstream target:", target.Host+":"+target.Port)
					}
				}
			}
		}
	}

	// Parse proxy_pass directives, but skip upstream references
	proxyPassRegex := regexp.MustCompile(`(?m)^\s*proxy_pass\s+([^;]+);`)
	proxyMatches := proxyPassRegex.FindAllStringSubmatch(content, -1)
	logger.Debug("ParseProxyTargetsFromRawContent: Found", len(proxyMatches), "proxy_pass directives")

	for _, match := range proxyMatches {
		if len(match) >= 2 {
			proxyPassURL := strings.TrimSpace(match[1])
			logger.Debug("ParseProxyTargetsFromRawContent: Processing proxy_pass:", proxyPassURL)
			// Skip if this proxy_pass references an upstream
			if !isUpstreamReference(proxyPassURL, allUpstreamNames) {
				target := parseProxyPassURL(proxyPassURL, "proxy_pass")
				if target.Host != "" {
					targets = append(targets, target)
					logger.Debug("ParseProxyTargetsFromRawContent: Added proxy_pass target:", target.Host+":"+target.Port)
				}
			} else {
				logger.Debug("ParseProxyTargetsFromRawContent: Skipping proxy_pass upstream reference:", proxyPassURL)
			}
		}
	}

	// Parse grpc_pass directives, but skip upstream references
	grpcPassRegex := regexp.MustCompile(`(?m)^\s*grpc_pass\s+([^;]+);`)
	grpcMatches := grpcPassRegex.FindAllStringSubmatch(content, -1)
	logger.Debug("ParseProxyTargetsFromRawContent: Found", len(grpcMatches), "grpc_pass directives")

	for _, match := range grpcMatches {
		if len(match) >= 2 {
			grpcPassURL := strings.TrimSpace(match[1])
			logger.Debug("ParseProxyTargetsFromRawContent: Processing grpc_pass:", grpcPassURL)
			// Skip if this grpc_pass references an upstream
			if !isUpstreamReference(grpcPassURL, allUpstreamNames) {
				target := parseProxyPassURL(grpcPassURL, "grpc_pass")
				if target.Host != "" {
					targets = append(targets, target)
					logger.Debug("ParseProxyTargetsFromRawContent: Added grpc_pass target:", target.Host+":"+target.Port)
				}
			} else {
				logger.Debug("ParseProxyTargetsFromRawContent: Skipping grpc_pass upstream reference:", grpcPassURL)
			}
		}
	}

	result := deduplicateTargets(targets)
	logger.Debug("ParseProxyTargetsFromRawContent: Final result - found", len(result), "unique targets")
	for i, target := range result {
		logger.Debugf("ParseProxyTargetsFromRawContent: Target %d: %s:%s (%s)", i+1, target.Host, target.Port, target.Type)
	}

	return result
}

// parseProxyPassURL parses a proxy_pass or grpc_pass URL and extracts host and port
func parseProxyPassURL(passURL, passType string) ProxyTarget {
	passURL = strings.TrimSpace(passURL)
	logger.Debug("parseProxyPassURL: Parsing URL:", passURL)

	// Skip URLs that contain Nginx variables
	if strings.Contains(passURL, "$") {
		logger.Debug("parseProxyPassURL: Skipping URL with Nginx variables:", passURL)
		return ProxyTarget{}
	}

	// Handle HTTP/HTTPS/gRPC URLs (e.g., "http://backend", "grpc://backend")
	if strings.HasPrefix(passURL, "http://") || strings.HasPrefix(passURL, "https://") || strings.HasPrefix(passURL, "grpc://") || strings.HasPrefix(passURL, "grpcs://") {
		if parsedURL, err := url.Parse(passURL); err == nil {
			host := parsedURL.Hostname()
			port := parsedURL.Port()
			logger.Debug("parseProxyPassURL: Parsed HTTP/HTTPS/gRPC URL:", passURL, "Host:", host, "Port:", port)

			// Set default ports if not specified
			if port == "" {
				switch parsedURL.Scheme {
				case "https":
					port = "443"
				case "grpcs":
					port = "443"
				case "grpc":
					port = "80"
				default: // http
					port = "80"
				}
				logger.Debug("parseProxyPassURL: Set default port for", passURL, "to:", port)
			}

			// Skip if this is the HTTP challenge port used by Let's Encrypt
			if host == "127.0.0.1" && port == settings.CertSettings.HTTPChallengePort {
				logger.Debug("parseProxyPassURL: Skipping HTTP challenge port for", passURL)
				return ProxyTarget{}
			}

			return ProxyTarget{
				Host: host,
				Port: port,
				Type: passType,
			}
		}
	}

	// Handle direct address format for stream module (e.g., "127.0.0.1:8080", "backend.example.com:12345")
	// This is used in stream configurations where proxy_pass/grpc_pass doesn't require a protocol
	if !strings.Contains(passURL, "://") {
		target := parseServerAddress(passURL, passType, nil) // No upstream context for this function
		logger.Debug("parseProxyPassURL: Parsed direct address URL:", passURL, "Target:", target.Host+":"+target.Port)

		// Skip if this is the HTTP challenge port used by Let's Encrypt
		if target.Host == "127.0.0.1" && target.Port == settings.CertSettings.HTTPChallengePort {
			logger.Debug("parseProxyPassURL: Skipping HTTP challenge port for", passURL)
			return ProxyTarget{}
		}

		return target
	}

	logger.Debug("parseProxyPassURL: Could not parse URL:", passURL)
	return ProxyTarget{}
}

// parseServerAddress parses upstream server address with upstream context
func parseServerAddress(serverAddr string, targetType string, ctx *UpstreamContext) ProxyTarget {
	serverAddr = strings.TrimSpace(serverAddr)
	logger.Debug("parseServerAddress: Parsing server address:", serverAddr)

	// Remove additional parameters (weight, max_fails, etc.)
	parts := strings.Fields(serverAddr)
	if len(parts) == 0 {
		logger.Debug("parseServerAddress: Empty server address, returning empty target")
		return ProxyTarget{}
	}

	addr := parts[0]
	target := ProxyTarget{
		Type: targetType,
	}

	// Add resolver information from upstream context
	if ctx != nil && ctx.Resolver != "" {
		target.Resolver = ctx.Resolver
		logger.Debug("parseServerAddress: Added resolver from upstream context for", addr, "to:", target.Resolver)
	}

	// Check if the address contains Nginx variables - skip if it does
	if strings.Contains(addr, "$") {
		logger.Debug("parseServerAddress: Skipping address with Nginx variables:", addr)
		return ProxyTarget{}
	}

	// Check for consul service discovery patterns
	if isConsulServiceDiscovery(serverAddr) {
		target.IsConsul = true
		target.ServiceURL = serverAddr
		logger.Debug("parseServerAddress: Found Consul service discovery for", addr, "Target:", target.Host+":"+target.Port)

		// Extract consul DNS host (e.g., "service.consul")
		if strings.Contains(addr, "service.consul") {
			target.Host = "service.consul"
			// For consul service discovery, we use a placeholder port since the actual port is dynamic
			target.Port = "dynamic"
			logger.Debug("parseServerAddress: Consul service discovery, host:", target.Host, "port:", target.Port)
		} else {
			// Fallback to regular parsing
			parsed := parseAddressOnly(addr)
			target.Host = parsed.Host
			target.Port = parsed.Port
			logger.Debug("parseServerAddress: Fallback to regular parsing for", addr, "Target:", target.Host+":"+target.Port)
		}

		return target
	}

	// Regular address parsing
	parsed := parseAddressOnly(addr)
	target.Host = parsed.Host
	target.Port = parsed.Port
	logger.Debug("parseServerAddress: Parsed regular address:", addr, "Target:", target.Host+":"+target.Port)

	// Skip if this is the HTTP challenge port used by Let's Encrypt
	if target.Host == "127.0.0.1" && target.Port == settings.CertSettings.HTTPChallengePort {
		logger.Debug("parseServerAddress: Skipping HTTP challenge port for", addr)
		return ProxyTarget{}
	}

	return target
}

// isConsulServiceDiscovery checks if the server address is a dynamic service discovery configuration
// This includes both Consul and standard nginx service= configurations
func isConsulServiceDiscovery(serverAddr string) bool {
	// Standard nginx service= format: "hostname service=name resolve"
	if strings.Contains(serverAddr, "service=") && strings.Contains(serverAddr, "resolve") {
		return true
	}
	// Legacy consul format: "service.consul service=name resolve"
	return strings.Contains(serverAddr, "service.consul") &&
		(strings.Contains(serverAddr, "service=") || strings.Contains(serverAddr, "resolve"))
}

// parseAddressOnly parses just the address portion without consul-specific logic
func parseAddressOnly(addr string) ProxyTarget {
	// Handle IPv6 addresses
	if strings.HasPrefix(addr, "[") {
		// IPv6 format: [::1]:8080
		if idx := strings.LastIndex(addr, "]:"); idx != -1 {
			host := addr[1:idx]
			port := addr[idx+2:]
			logger.Debug("parseAddressOnly: Parsed IPv6 address:", addr, "Host:", host, "Port:", port)
			return ProxyTarget{
				Host: host,
				Port: port,
			}
		}
		// IPv6 without port: [::1]
		host := strings.Trim(addr, "[]")
		logger.Debug("parseAddressOnly: Parsed IPv6 address without port:", addr, "Host:", host)
		return ProxyTarget{
			Host: host,
			Port: "80",
		}
	}

	// Handle IPv4 addresses and hostnames
	if strings.Contains(addr, ":") {
		parts := strings.Split(addr, ":")
		if len(parts) == 2 {
			logger.Debug("parseAddressOnly: Parsed IPv4/Hostname address:", addr, "Host:", parts[0], "Port:", parts[1])
			return ProxyTarget{
				Host: parts[0],
				Port: parts[1],
			}
		}
	}

	// No port specified, use default
	logger.Debug("parseAddressOnly: Parsed address without port:", addr, "Host:", addr, "Port:", "80")
	return ProxyTarget{
		Host: addr,
		Port: "80",
	}
}

// deduplicateTargets removes duplicate proxy targets
func deduplicateTargets(targets []ProxyTarget) []ProxyTarget {
	seen := make(map[string]bool)
	var result []ProxyTarget

	for _, target := range targets {
		// Create a unique key that includes resolver and consul information
		key := target.Host + ":" + target.Port + ":" + target.Type + ":" + target.Resolver
		if target.IsConsul {
			key += ":consul:" + target.ServiceURL
		}
		logger.Debug("deduplicateTargets: Checking for duplicates, current key:", key)

		if !seen[key] {
			seen[key] = true
			result = append(result, target)
			logger.Debug("deduplicateTargets: Added unique target:", target.Host+":"+target.Port)
		} else {
			logger.Debug("deduplicateTargets: Skipping duplicate target:", target.Host+":"+target.Port)
		}
	}

	return result
}

// isUpstreamReference checks if a proxy_pass or grpc_pass URL references an upstream block
func isUpstreamReference(passURL string, upstreamNames map[string]bool) bool {
	passURL = strings.TrimSpace(passURL)
	logger.Debug("isUpstreamReference: Checking if URL is an upstream reference:", passURL)

	// For HTTP/HTTPS/gRPC URLs, parse the URL to extract the hostname
	if strings.HasPrefix(passURL, "http://") || strings.HasPrefix(passURL, "https://") || strings.HasPrefix(passURL, "grpc://") || strings.HasPrefix(passURL, "grpcs://") {
		// Handle URLs with nginx variables (e.g., "https://myUpStr$request_uri")
		// Extract the scheme and hostname part before any nginx variables
		schemeAndHost := passURL
		if dollarIndex := strings.Index(passURL, "$"); dollarIndex != -1 {
			schemeAndHost = passURL[:dollarIndex]
		}
		logger.Debug("isUpstreamReference: Handling URL with Nginx variable:", passURL, "SchemeAndHost:", schemeAndHost)

		// Try to parse the URL, if it fails, try manual extraction
		if parsedURL, err := url.Parse(schemeAndHost); err == nil {
			hostname := parsedURL.Hostname()
			logger.Debug("isUpstreamReference: Parsed URL for upstream check:", passURL, "Hostname:", hostname)
			// Check if the hostname matches any upstream name
			return upstreamNames[hostname]
		} else {
			logger.Debug("isUpstreamReference: Failed to parse URL for upstream check:", passURL, "Error:", err)
			// Fallback: manually extract hostname for URLs with variables
			// Remove scheme prefix
			withoutScheme := passURL
			if strings.HasPrefix(passURL, "https://") {
				withoutScheme = strings.TrimPrefix(passURL, "https://")
			} else if strings.HasPrefix(passURL, "http://") {
				withoutScheme = strings.TrimPrefix(passURL, "http://")
			} else if strings.HasPrefix(passURL, "grpc://") {
				withoutScheme = strings.TrimPrefix(passURL, "grpc://")
			} else if strings.HasPrefix(passURL, "grpcs://") {
				withoutScheme = strings.TrimPrefix(passURL, "grpcs://")
			}
			logger.Debug("isUpstreamReference: Manual hostname extraction for URL with variable:", passURL, "WithoutScheme:", withoutScheme)

			// Extract hostname before any path, port, or variable
			hostname := withoutScheme
			if slashIndex := strings.Index(hostname, "/"); slashIndex != -1 {
				hostname = hostname[:slashIndex]
			}
			if colonIndex := strings.Index(hostname, ":"); colonIndex != -1 {
				hostname = hostname[:colonIndex]
			}
			if dollarIndex := strings.Index(hostname, "$"); dollarIndex != -1 {
				hostname = hostname[:dollarIndex]
			}
			logger.Debug("isUpstreamReference: Final hostname for upstream check:", passURL, "Hostname:", hostname)

			return upstreamNames[hostname]
		}
	}

	// For stream module, proxy_pass/grpc_pass can directly reference upstream name without protocol
	// Check if the pass value directly matches an upstream name
	if !strings.Contains(passURL, "://") && !strings.Contains(passURL, ":") {
		logger.Debug("isUpstreamReference: Directly referencing upstream name:", passURL)
		return upstreamNames[passURL]
	}

	logger.Debug("isUpstreamReference: URL is not an upstream reference:", passURL)
	return false
}
