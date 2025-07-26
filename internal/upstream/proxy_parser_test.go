package upstream

import (
	"testing"
)

func TestParseProxyTargetsFromRawContent(t *testing.T) {
	config := `map $http_upgrade $connection_upgrade {
    default upgrade;
    '' close;
}
upstream api-1 {
    server 127.0.0.1:9000;
    server 127.0.0.1:443;
}
upstream api-2 {
    server 127.0.0.1:9003;
    server 127.0.0.1:9005;
}
server {
    listen 80;
    listen [::]:80;
    server_name test.jackyu.cn;
    location / {
        # First attempt to serve request as file, then
        # as directory, then fall back to displaying a 404.
        index index.html;
        try_files $uri $uri/ /index.html;
    }
    location /admin {
        index admin.html;
        try_files $uri $uri/ /admin.html;
    }
    location /user {
        index user.html;
        try_files $uri $uri/ /user.html;
    }
    location /api/ {
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_pass http://api-1/;
        proxy_redirect off;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        client_max_body_size 1000m;
    }
}
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    server_name test.jackyu.cn;
    ssl_certificate /etc/nginx/ssl/test.jackyu.cn_P256/fullchain.cer;
    ssl_certificate_key /etc/nginx/ssl/test.jackyu.cn_P256/private.key;
    root /var/www/ibeta/html;
    index index.html;
    http2 on;
    access_log /var/log/nginx/test.jackyu.cn.log main;
    location / {
        # First attempt to serve request as file, then
        # as directory, then fall back to displaying a 404.
        index index.html;
        try_files $uri $uri/ /index.html;
    }
    location /admin {
        index admin.html;
        try_files $uri $uri/ /admin.html;
    }
    location /user {
        index user.html;
        try_files $uri $uri/ /user.html;
    }
    location /api/ {
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_pass http://api-1/;
        proxy_redirect off;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        client_max_body_size 100m;
    }
}`

	targets := ParseProxyTargetsFromRawContent(config)

	// Expected targets: 4 upstream servers (2 from api-1, 2 from api-2)
	// proxy_pass http://api-1/ should be ignored since it references an upstream
	expectedTargets := []ProxyTarget{
		{Host: "127.0.0.1", Port: "9000", Type: "upstream"},
		{Host: "127.0.0.1", Port: "443", Type: "upstream"},
		{Host: "127.0.0.1", Port: "9003", Type: "upstream"},
		{Host: "127.0.0.1", Port: "9005", Type: "upstream"},
	}

	if len(targets) != len(expectedTargets) {
		t.Errorf("Expected %d targets, got %d", len(expectedTargets), len(targets))
		for i, target := range targets {
			t.Logf("Target %d: %+v", i, target)
		}
		return
	}

	// Create a map for easier comparison
	targetMap := make(map[string]ProxyTarget)
	for _, target := range targets {
		key := target.Host + ":" + target.Port + ":" + target.Type
		targetMap[key] = target
	}

	for _, expected := range expectedTargets {
		key := expected.Host + ":" + expected.Port + ":" + expected.Type
		if _, found := targetMap[key]; !found {
			t.Errorf("Expected target not found: %+v", expected)
		}
	}
}

func TestIsUpstreamReference(t *testing.T) {
	upstreamNames := map[string]bool{
		"api-1":   true,
		"api-2":   true,
		"backend": true,
		"myUpStr": true,
	}

	tests := []struct {
		proxyPass string
		expected  bool
	}{
		{"http://api-1/", true},
		{"http://api-1", true},
		{"https://api-2/path", true},
		{"http://backend", true},
		{"http://127.0.0.1:8080", false},
		{"https://example.com", false},
		{"http://unknown-upstream", false},
		// Test cases for nginx variables
		{"https://myUpStr$request_uri", true},
		{"http://api-1$request_uri", true},
		{"https://backend$server_name", true},
		{"http://unknown-upstream$request_uri", false},
		{"https://example.com$request_uri", false},
		// Test cases for URLs with variables and paths
		{"https://myUpStr/api$request_uri", true},
		{"http://api-1:8080$request_uri", true},
	}

	for _, test := range tests {
		result := isUpstreamReference(test.proxyPass, upstreamNames)
		if result != test.expected {
			t.Errorf("isUpstreamReference(%q) = %v, expected %v", test.proxyPass, result, test.expected)
		}
	}
}

func TestParseProxyTargetsWithDirectProxyPass(t *testing.T) {
	config := `upstream api-1 {
    server 127.0.0.1:9000;
    server 127.0.0.1:443;
}
server {
    listen 80;
    server_name test.jackyu.cn;
    location /api/ {
        proxy_pass http://api-1/;
    }
    location /external/ {
        proxy_pass http://external.example.com:8080/;
    }
    location /another/ {
        proxy_pass https://another.example.com/;
    }
}`

	targets := ParseProxyTargetsFromRawContent(config)

	// Expected targets:
	// - 2 upstream servers from api-1
	// - 2 direct proxy_pass targets (external.example.com:8080, another.example.com:443)
	// - proxy_pass http://api-1/ should be ignored since it references an upstream
	expectedTargets := []ProxyTarget{
		{Host: "127.0.0.1", Port: "9000", Type: "upstream"},
		{Host: "127.0.0.1", Port: "443", Type: "upstream"},
		{Host: "external.example.com", Port: "8080", Type: "proxy_pass"},
		{Host: "another.example.com", Port: "443", Type: "proxy_pass"},
	}

	if len(targets) != len(expectedTargets) {
		t.Errorf("Expected %d targets, got %d", len(expectedTargets), len(targets))
		for i, target := range targets {
			t.Logf("Target %d: %+v", i, target)
		}
		return
	}

	// Create a map for easier comparison
	targetMap := make(map[string]ProxyTarget)
	for _, target := range targets {
		key := target.Host + ":" + target.Port + ":" + target.Type
		targetMap[key] = target
	}

	for _, expected := range expectedTargets {
		key := expected.Host + ":" + expected.Port + ":" + expected.Type
		if _, found := targetMap[key]; !found {
			t.Errorf("Expected target not found: %+v", expected)
		}
	}
}

func TestParseProxyTargetsFromStreamConfig(t *testing.T) {
	config := `upstream backend {
    server 127.0.0.1:9000;
    server 127.0.0.1:9001;
}

server {
    listen 12345;
    proxy_pass backend;
}

server {
    listen 12346;
    proxy_pass 192.168.1.100:8080;
}

server {
    listen 12347;
    proxy_pass example.com:3306;
}`

	targets := ParseProxyTargetsFromRawContent(config)

	// Expected targets:
	// - 2 upstream servers from backend
	// - 2 direct proxy_pass targets (192.168.1.100:8080, example.com:3306)
	// - proxy_pass backend should be ignored since it references an upstream
	expectedTargets := []ProxyTarget{
		{Host: "127.0.0.1", Port: "9000", Type: "upstream"},
		{Host: "127.0.0.1", Port: "9001", Type: "upstream"},
		{Host: "192.168.1.100", Port: "8080", Type: "proxy_pass"},
		{Host: "example.com", Port: "3306", Type: "proxy_pass"},
	}

	if len(targets) != len(expectedTargets) {
		t.Errorf("Expected %d targets, got %d", len(expectedTargets), len(targets))
		for i, target := range targets {
			t.Logf("Target %d: %+v", i, target)
		}
		return
	}

	// Create a map for easier comparison
	targetMap := make(map[string]ProxyTarget)
	for _, target := range targets {
		key := target.Host + ":" + target.Port + ":" + target.Type
		targetMap[key] = target
	}

	for _, expected := range expectedTargets {
		key := expected.Host + ":" + expected.Port + ":" + expected.Type
		if _, found := targetMap[key]; !found {
			t.Errorf("Expected target not found: %+v", expected)
		}
	}
}

func TestParseProxyTargetsFromMixedConfig(t *testing.T) {
	config := `upstream web_backend {
    server web1.example.com:80;
    server web2.example.com:80;
}

upstream stream_backend {
    server stream1.example.com:12345;
    server stream2.example.com:12345;
}

# HTTP server block
server {
    listen 80;
    server_name example.com;
    location / {
        proxy_pass http://web_backend/;
    }
    location /api {
        proxy_pass http://api.example.com:8080/;
    }
}

# Stream server blocks
server {
    listen 12345;
    proxy_pass stream_backend;
}

server {
    listen 3306;
    proxy_pass mysql.example.com:3306;
}`

	targets := ParseProxyTargetsFromRawContent(config)

	// Expected targets:
	// - 2 upstream servers from web_backend
	// - 2 upstream servers from stream_backend
	// - 1 direct HTTP proxy_pass (api.example.com:8080)
	// - 1 direct stream proxy_pass (mysql.example.com:3306)
	// - proxy_pass http://web_backend/ and proxy_pass stream_backend should be ignored
	expectedTargets := []ProxyTarget{
		{Host: "web1.example.com", Port: "80", Type: "upstream"},
		{Host: "web2.example.com", Port: "80", Type: "upstream"},
		{Host: "stream1.example.com", Port: "12345", Type: "upstream"},
		{Host: "stream2.example.com", Port: "12345", Type: "upstream"},
		{Host: "api.example.com", Port: "8080", Type: "proxy_pass"},
		{Host: "mysql.example.com", Port: "3306", Type: "proxy_pass"},
	}

	if len(targets) != len(expectedTargets) {
		t.Errorf("Expected %d targets, got %d", len(expectedTargets), len(targets))
		for i, target := range targets {
			t.Logf("Target %d: %+v", i, target)
		}
		return
	}

	// Create a map for easier comparison
	targetMap := make(map[string]ProxyTarget)
	for _, target := range targets {
		key := target.Host + ":" + target.Port + ":" + target.Type
		targetMap[key] = target
	}

	for _, expected := range expectedTargets {
		key := expected.Host + ":" + expected.Port + ":" + expected.Type
		if _, found := targetMap[key]; !found {
			t.Errorf("Expected target not found: %+v", expected)
		}
	}
}

func TestParseProxyTargetsFromUserConfig(t *testing.T) {
	config := `upstream my-tcp {
    server 127.0.0.1:9000;
}
server {
    listen 1234-1236;
    resolver 8.8.8.8 valid=1s;
    proxy_pass example.com:$server_port;
}`

	targets := ParseProxyTargetsFromRawContent(config)

	// Print actual results for debugging
	t.Logf("Found %d targets:", len(targets))
	for i, target := range targets {
		t.Logf("Target %d: Host=%s, Port=%s, Type=%s", i+1, target.Host, target.Port, target.Type)
	}

	// Expected targets:
	// - 1 upstream server from my-tcp
	// - proxy_pass example.com:$server_port should be filtered out due to Nginx variable
	expectedTargets := []ProxyTarget{
		{Host: "127.0.0.1", Port: "9000", Type: "upstream"},
	}

	if len(targets) != len(expectedTargets) {
		t.Errorf("Expected %d targets, got %d", len(expectedTargets), len(targets))
		return
	}

	// Create a map for easier comparison
	targetMap := make(map[string]ProxyTarget)
	for _, target := range targets {
		key := target.Host + ":" + target.Port + ":" + target.Type
		targetMap[key] = target
	}

	for _, expected := range expectedTargets {
		key := expected.Host + ":" + expected.Port + ":" + expected.Type
		if _, found := targetMap[key]; !found {
			t.Errorf("Expected target not found: %+v", expected)
		}
	}
}

func TestParseProxyTargetsWithNginxVariables(t *testing.T) {
	config := `map $http_upgrade $connection_upgrade {
    default upgrade;
    '' close;
}
upstream myUpStr {
    keepalive 32;
    keepalive_timeout 600s;
    server 192.168.1.100:8080;
}
server {
    listen 80;
    listen [::]:80;
    server_name my.domain.tld;
    return 307 https://$server_name$request_uri;
}
server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name my.domain.tld;
    ssl_certificate /path/to/cert;
    ssl_certificate_key /path/to/key;
    location / {
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        client_max_body_size 1000m;
        proxy_redirect off;
        add_header X-Served-By $host;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Scheme $scheme;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-Host $host:$server_port;
        proxy_set_header X-Forwarded-Server $host;
        proxy_pass https://myUpStr$request_uri;
    }
}`

	targets := ParseProxyTargetsFromRawContent(config)

	// Expected targets:
	// - 1 upstream server from myUpStr
	// - proxy_pass https://myUpStr$request_uri should be ignored since it references an upstream
	expectedTargets := []ProxyTarget{
		{Host: "192.168.1.100", Port: "8080", Type: "upstream"},
	}

	if len(targets) != len(expectedTargets) {
		t.Errorf("Expected %d targets, got %d", len(expectedTargets), len(targets))
		for i, target := range targets {
			t.Logf("Target %d: %+v", i, target)
		}
		return
	}

	// Create a map for easier comparison
	targetMap := make(map[string]ProxyTarget)
	for _, target := range targets {
		key := target.Host + ":" + target.Port + ":" + target.Type
		targetMap[key] = target
	}

	for _, expected := range expectedTargets {
		key := expected.Host + ":" + expected.Port + ":" + expected.Type
		if _, found := targetMap[key]; !found {
			t.Errorf("Expected target not found: %+v", expected)
		}
	}
}

func TestParseProxyTargetsWithComplexNginxVariables(t *testing.T) {
	config := `upstream backend_api {
    server api1.example.com:8080;
    server api2.example.com:8080;
}

upstream backend_ws {
    server ws1.example.com:9000;
    server ws2.example.com:9000;
}

server {
    listen 80;
    server_name example.com;

    location /api/ {
        proxy_pass http://backend_api$request_uri;
    }

    location /ws/ {
        proxy_pass http://backend_ws/websocket$request_uri;
    }

    location /external/ {
        proxy_pass https://external.example.com:8443$request_uri;
    }

    location /static/ {
        proxy_pass http://static.example.com$uri;
    }
}`

	targets := ParseProxyTargetsFromRawContent(config)

	// Expected targets:
	// - 2 upstream servers from backend_api
	// - 2 upstream servers from backend_ws
	// - proxy_pass with upstream references should be ignored
	// - proxy_pass with variables should be filtered out
	expectedTargets := []ProxyTarget{
		{Host: "api1.example.com", Port: "8080", Type: "upstream"},
		{Host: "api2.example.com", Port: "8080", Type: "upstream"},
		{Host: "ws1.example.com", Port: "9000", Type: "upstream"},
		{Host: "ws2.example.com", Port: "9000", Type: "upstream"},
	}

	if len(targets) != len(expectedTargets) {
		t.Errorf("Expected %d targets, got %d", len(expectedTargets), len(targets))
		for i, target := range targets {
			t.Logf("Target %d: %+v", i, target)
		}
		return
	}

	// Create a map for easier comparison
	targetMap := make(map[string]ProxyTarget)
	for _, target := range targets {
		key := target.Host + ":" + target.Port + ":" + target.Type
		targetMap[key] = target
	}

	for _, expected := range expectedTargets {
		key := expected.Host + ":" + expected.Port + ":" + expected.Type
		if _, found := targetMap[key]; !found {
			t.Errorf("Expected target not found: %+v", expected)
		}
	}
}

func TestFilterNginxVariables(t *testing.T) {
	config := `upstream backend {
    server 192.168.1.100:8080;
    server api.example.com:$custom_port;  # Should be filtered out
    server $backend_host:9000;            # Should be filtered out
}

server {
    listen 80;
    location /api {
        proxy_pass http://api.example.com:$server_port/;  # Should be filtered out
    }
    location /static {
        proxy_pass http://static.example.com:8080/;       # Should be kept
    }
}

server {
    listen 3306;
    proxy_pass mysql.$domain:3306;        # Should be filtered out
}

server {
    listen 5432;
    proxy_pass postgres.example.com:5432; # Should be kept
}`

	targets := ParseProxyTargetsFromRawContent(config)

	// Print actual results for debugging
	t.Logf("Found %d targets:", len(targets))
	for i, target := range targets {
		t.Logf("Target %d: Host=%s, Port=%s, Type=%s", i+1, target.Host, target.Port, target.Type)
	}

	// Expected targets: only those without Nginx variables
	expectedTargets := []ProxyTarget{
		{Host: "192.168.1.100", Port: "8080", Type: "upstream"},
		{Host: "static.example.com", Port: "8080", Type: "proxy_pass"},
		{Host: "postgres.example.com", Port: "5432", Type: "proxy_pass"},
	}

	if len(targets) != len(expectedTargets) {
		t.Errorf("Expected %d targets, got %d", len(expectedTargets), len(targets))
		return
	}

	// Create a map for easier comparison
	targetMap := make(map[string]ProxyTarget)
	for _, target := range targets {
		key := target.Host + ":" + target.Port + ":" + target.Type
		targetMap[key] = target
	}

	for _, expected := range expectedTargets {
		key := expected.Host + ":" + expected.Port + ":" + expected.Type
		if _, found := targetMap[key]; !found {
			t.Errorf("Expected target not found: %+v", expected)
		}
	}

	// Verify that targets with variables are filtered out
	variableTargets := []string{
		"api.example.com:$custom_port:upstream",
		"$backend_host:9000:upstream",
		"api.example.com:$server_port:proxy_pass",
		"mysql.$domain:3306:proxy_pass",
	}

	for _, varTarget := range variableTargets {
		if _, found := targetMap[varTarget]; found {
			t.Errorf("Variable target should have been filtered out but was found: %s", varTarget)
		}
	}
}

func TestParseGrpcPassDirectives(t *testing.T) {
	config := `
upstream grpc-backend {
    server 127.0.0.1:9090;
    server 127.0.0.1:9091;
}

server {
    listen 80 http2;
    server_name grpc.example.com;

    location /api.v1.Service/ {
        grpc_pass grpc://127.0.0.1:9090;
    }

    location /api.v2.Service/ {
        grpc_pass grpcs://secure-grpc.example.com:443;
    }

    location /upstream-service/ {
        grpc_pass grpc://grpc-backend;
    }

    location /direct-service/ {
        grpc_pass 192.168.1.100:9090;
    }
}
`

	targets := ParseProxyTargetsFromRawContent(config)

	// Verify we found the expected targets
	expected := []struct {
		host string
		port string
		typ  string
	}{
		{"127.0.0.1", "9090", "upstream"},
		{"127.0.0.1", "9091", "upstream"},
		{"127.0.0.1", "9090", "grpc_pass"},
		{"secure-grpc.example.com", "443", "grpc_pass"},
		{"192.168.1.100", "9090", "grpc_pass"},
	}

	if len(targets) < len(expected) {
		t.Errorf("Expected at least %d targets, got %d", len(expected), len(targets))
		for i, target := range targets {
			t.Logf("Target %d: Host=%s, Port=%s, Type=%s", i+1, target.Host, target.Port, target.Type)
		}
		return
	}

	// Count targets by type
	grpcPassCount := 0
	upstreamCount := 0
	for _, target := range targets {
		switch target.Type {
		case "grpc_pass":
			grpcPassCount++
		case "upstream":
			upstreamCount++
		}
	}

	if grpcPassCount != 3 {
		t.Errorf("Expected 3 grpc_pass targets, got %d", grpcPassCount)
	}
	if upstreamCount != 2 {
		t.Errorf("Expected 2 upstream targets, got %d", upstreamCount)
	}

	// Verify specific targets exist
	found := make(map[string]bool)
	for _, target := range targets {
		key := target.Host + ":" + target.Port + ":" + target.Type
		found[key] = true
	}

	expectedKeys := []string{
		"127.0.0.1:9090:upstream",
		"127.0.0.1:9091:upstream",
		"127.0.0.1:9090:grpc_pass",
		"secure-grpc.example.com:443:grpc_pass",
		"192.168.1.100:9090:grpc_pass",
	}

	for _, key := range expectedKeys {
		if !found[key] {
			t.Errorf("Expected to find target: %s", key)
		}
	}
}

func TestGrpcPassPortDefaults(t *testing.T) {
	tests := []struct {
		name         string
		grpcPassURL  string
		expectedHost string
		expectedPort string
		expectedType string
	}{
		{
			name:         "grpc:// without port should default to 80",
			grpcPassURL:  "grpc://api.example.com",
			expectedHost: "api.example.com",
			expectedPort: "80",
			expectedType: "grpc_pass",
		},
		{
			name:         "grpcs:// without port should default to 443",
			grpcPassURL:  "grpcs://secure-api.example.com",
			expectedHost: "secure-api.example.com",
			expectedPort: "443",
			expectedType: "grpc_pass",
		},
		{
			name:         "grpc:// with explicit port",
			grpcPassURL:  "grpc://api.example.com:9090",
			expectedHost: "api.example.com",
			expectedPort: "9090",
			expectedType: "grpc_pass",
		},
		{
			name:         "grpcs:// with explicit port",
			grpcPassURL:  "grpcs://secure-api.example.com:9443",
			expectedHost: "secure-api.example.com",
			expectedPort: "9443",
			expectedType: "grpc_pass",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := parseProxyPassURL(tt.grpcPassURL, "grpc_pass")

			if target.Host != tt.expectedHost {
				t.Errorf("Expected host %s, got %s", tt.expectedHost, target.Host)
			}
			if target.Port != tt.expectedPort {
				t.Errorf("Expected port %s, got %s", tt.expectedPort, target.Port)
			}
			if target.Type != tt.expectedType {
				t.Errorf("Expected type %s, got %s", tt.expectedType, target.Type)
			}
		})
	}
}

func TestParseProxyTargetsFromMultipleConfigs(t *testing.T) {
	// Simulate your exact use case:
	// A.conf contains upstream definition
	// B.conf contains proxy_pass/grpc_pass that references the upstream
	configContents := map[string]string{
		"/etc/nginx/conf.d/A.conf": `
upstream sunstore {
    keepalive 100;
    server 120.78.154.134:443;
}

upstream asr {
    server localhost:50051;
    server localhost:50052;
}
`,
		"/etc/nginx/conf.d/B.conf": `
server {
    listen 80;
    server_name example.com;
    location /api {
        proxy_pass https://sunstore;
    }
    location /grpc {
        grpc_pass grpcs://asr;
    }
}
`,
		"/etc/nginx/conf.d/C.conf": `
server {
    listen 443 ssl;
    server_name grpc.example.com;
    location /direct {
        proxy_pass http://192.168.1.100:8080;
    }
    location /grpc-direct {
        grpc_pass grpc://api.example.com:9090;
    }
}
`,
	}

	targets := ParseProxyTargetsFromMultipleConfigs(configContents)

	t.Logf("Found %d targets from multiple configs:", len(targets))
	for i, target := range targets {
		t.Logf("Target %d: Host=%s, Port=%s, Type=%s",
			i+1, target.Host, target.Port, target.Type)
	}

	// Expected targets:
	// 1. From upstream sunstore: 120.78.154.134:443 (upstream)
	// 2. From upstream asr: localhost:50051 (upstream)
	// 3. From upstream asr: localhost:50052 (upstream)
	// 4. From direct proxy_pass: 192.168.1.100:8080 (proxy_pass)
	// 5. From direct grpc_pass: api.example.com:9090 (grpc_pass)
	// NOT included: https://sunstore and grpcs://asr (these are upstream references)

	expectedTargets := map[string]string{
		"120.78.154.134:443":   "upstream",
		"localhost:50051":      "upstream",
		"localhost:50052":      "upstream",
		"192.168.1.100:8080":   "proxy_pass",
		"api.example.com:9090": "grpc_pass",
	}

	if len(targets) != len(expectedTargets) {
		t.Errorf("Expected %d targets, got %d", len(expectedTargets), len(targets))
	}

	found := make(map[string]string)
	for _, target := range targets {
		key := target.Host + ":" + target.Port
		found[key] = target.Type
	}

	for expectedKey, expectedType := range expectedTargets {
		if foundType, exists := found[expectedKey]; !exists {
			t.Errorf("Expected to find target: %s with type %s", expectedKey, expectedType)
		} else if foundType != expectedType {
			t.Errorf("Target %s: expected type %s, got %s", expectedKey, expectedType, foundType)
		}
	}

	// Verify that upstream references are NOT included as separate targets
	upstreamRefs := []string{"sunstore:443", "asr:443"}
	for _, ref := range upstreamRefs {
		if _, exists := found[ref]; exists {
			t.Errorf("Upstream reference %s should NOT be included as a separate target", ref)
		}
	}
}

func TestCrossFileUpstreamReferenceBugFix(t *testing.T) {
	// This test specifically addresses the bug you reported:
	// upstream sunstore in A.conf, proxy_pass https://sunstore in B.conf
	// should resolve to 120.78.154.134:443, not sunstore:443

	configContents := map[string]string{
		"A.conf": `upstream sunstore {
    keepalive 100;
    server 120.78.154.134:443;
}`,
		"B.conf": `server {
    listen 80;
    location / {
        proxy_pass https://sunstore;
    }
}`,
	}

	targets := ParseProxyTargetsFromMultipleConfigs(configContents)

	t.Logf("Bug fix test - Found %d targets:", len(targets))
	for i, target := range targets {
		t.Logf("Target %d: Host=%s, Port=%s, Type=%s",
			i+1, target.Host, target.Port, target.Type)
	}

	// Should only find the actual upstream server, not the reference
	if len(targets) != 1 {
		t.Errorf("Expected exactly 1 target (the upstream server), got %d", len(targets))
	}

	if len(targets) > 0 {
		target := targets[0]
		if target.Host != "120.78.154.134" || target.Port != "443" || target.Type != "upstream" {
			t.Errorf("Expected target 120.78.154.134:443 (upstream), got %s:%s (%s)",
				target.Host, target.Port, target.Type)
		}
	}
}

func TestCrossFileUpstreamReferenceFixed(t *testing.T) {
	// This test verifies that the fix for cross-file upstream references works correctly
	// Simulate the service scanning multiple config files sequentially

	service := GetUpstreamService()

	// Clear any existing state
	service.targetsMutex.Lock()
	service.globalUpstreams = make(map[string]bool)
	service.targets = make(map[string]*TargetInfo)
	service.targetsMutex.Unlock()

	// First config file (A.conf) - contains upstream definition
	configA := `upstream sunstore {
    keepalive 100;
    server 120.78.154.134:443;
}

upstream asr {
    server localhost:50051;
    server localhost:50052;
}`

	// Simulate scanning config A (this updates global upstreams)
	service.updateUpstreamDefinitions("/etc/nginx/conf.d/A.conf", configA)

	// Verify global upstreams were updated
	globalUpstreams := service.GetGlobalUpstreams()
	if !globalUpstreams["sunstore"] {
		t.Error("Expected sunstore upstream to be registered globally")
	}
	if !globalUpstreams["asr"] {
		t.Error("Expected asr upstream to be registered globally")
	}

	// Second config file (B.conf) - contains proxy_pass/grpc_pass that reference upstreams
	configB := `server {
    listen 80;
    server_name example.com;
    location /api {
        proxy_pass https://sunstore;
    }
    location /grpc {
        grpc_pass grpcs://asr;
    }
    location /direct {
        proxy_pass http://192.168.1.100:8080;
    }
}`

	// Parse config B - should NOT create targets for upstream references
	targets := ParseProxyTargetsFromRawContent(configB)

	t.Logf("Found %d targets in config B:", len(targets))
	for i, target := range targets {
		t.Logf("  Target %d: %s:%s (%s)", i+1, target.Host, target.Port, target.Type)
	}

	// We should only find the direct proxy target, not the upstream references
	expectedTargets := []struct {
		host string
		port string
		typ  string
	}{
		{"192.168.1.100", "8080", "proxy_pass"},
	}

	if len(targets) != len(expectedTargets) {
		t.Errorf("Expected %d targets, got %d", len(expectedTargets), len(targets))
	}

	for i, expected := range expectedTargets {
		if i >= len(targets) {
			t.Errorf("Missing target %d: %s:%s (%s)", i+1, expected.host, expected.port, expected.typ)
			continue
		}

		target := targets[i]
		if target.Host != expected.host || target.Port != expected.port || target.Type != expected.typ {
			t.Errorf("Target %d mismatch: expected %s:%s (%s), got %s:%s (%s)",
				i+1, expected.host, expected.port, expected.typ,
				target.Host, target.Port, target.Type)
		}
	}

	// Verify that https://sunstore and grpcs://asr were correctly identified as upstream references
	// (they should NOT appear in the targets list)
	for _, target := range targets {
		if target.Host == "sunstore" {
			t.Error("sunstore should not appear as a target (it's an upstream reference)")
		}
		if target.Host == "asr" {
			t.Error("asr should not appear as a target (it's an upstream reference)")
		}
	}

	// Now parse config A to get the actual upstream servers
	targetsA := ParseProxyTargetsFromRawContent(configA)

	t.Logf("Found %d targets in config A:", len(targetsA))
	for i, target := range targetsA {
		t.Logf("  Target %d: %s:%s (%s)", i+1, target.Host, target.Port, target.Type)
	}

	// Config A should contain the actual upstream servers
	expectedUpstreamTargets := []struct {
		host string
		port string
		typ  string
	}{
		{"120.78.154.134", "443", "upstream"},
		{"localhost", "50051", "upstream"},
		{"localhost", "50052", "upstream"},
	}

	if len(targetsA) != len(expectedUpstreamTargets) {
		t.Errorf("Expected %d upstream targets, got %d", len(expectedUpstreamTargets), len(targetsA))
	}

	for i, expected := range expectedUpstreamTargets {
		if i >= len(targetsA) {
			t.Errorf("Missing upstream target %d: %s:%s (%s)", i+1, expected.host, expected.port, expected.typ)
			continue
		}

		target := targetsA[i]
		if target.Host != expected.host || target.Port != expected.port || target.Type != expected.typ {
			t.Errorf("Upstream target %d mismatch: expected %s:%s (%s), got %s:%s (%s)",
				i+1, expected.host, expected.port, expected.typ,
				target.Host, target.Port, target.Type)
		}
	}
}
