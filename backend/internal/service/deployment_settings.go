package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/ssh"
)

type DeploymentSettings struct {
	RepoURL             string `json:"repo_url"`
	Branch              string `json:"branch"`
	ServerHost          string `json:"server_host"`
	ServerPort          int    `json:"server_port"`
	ServerUsername      string `json:"server_username"`
	ServerPassword      string `json:"server_password,omitempty"`
	ServerPasswordSet   bool   `json:"server_password_set"`
	TargetPath          string `json:"target_path"`
	DeployCommand       string `json:"deploy_command"`
	BackendServiceName  string `json:"backend_service_name"`
	FrontendServiceName string `json:"frontend_service_name"`

	RedisHost     string `json:"redis_host"`
	RedisPort     int    `json:"redis_port"`
	RedisPassword string `json:"redis_password,omitempty"`
	RedisDB       int    `json:"redis_db"`

	PostgresHost        string `json:"postgres_host"`
	PostgresPort        int    `json:"postgres_port"`
	PostgresUser        string `json:"postgres_user"`
	PostgresPassword    string `json:"postgres_password,omitempty"`
	PostgresPasswordSet bool   `json:"postgres_password_set"`
	PostgresDBName      string `json:"postgres_db_name"`
	PostgresSSLMode     string `json:"postgres_ssl_mode"`
}

type DeploymentCheckItem struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type DeploymentTestResult struct {
	Items []DeploymentCheckItem `json:"items"`
}

type DeploymentRunResult struct {
	Output string `json:"output"`
}

const defaultDockerImageDeployCommand = "set -e; cd /opt/sub2api; git fetch origin main; git checkout main; git pull --ff-only origin main; cd deploy; docker compose -f docker-compose.local.yml -f docker-compose.image.override.yml -f docker-compose.override.yml pull sub2api; docker compose -f docker-compose.local.yml -f docker-compose.image.override.yml -f docker-compose.override.yml up -d --no-deps --force-recreate sub2api; sleep 8; docker compose -f docker-compose.local.yml -f docker-compose.image.override.yml -f docker-compose.override.yml ps sub2api; curl -i http://127.0.0.1:8080/health; docker logs --tail 80 sub2api"

func (s *SettingService) GetDeploymentSettings(ctx context.Context) (*DeploymentSettings, error) {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyDeploymentSettings)
	if err != nil {
		if err == ErrSettingNotFound {
			return defaultDeploymentSettings(), nil
		}
		return nil, fmt.Errorf("get deployment settings: %w", err)
	}
	cfg := defaultDeploymentSettings()
	if strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		return nil, fmt.Errorf("unmarshal deployment settings: %w", err)
	}
	cfg.ServerPasswordSet = strings.TrimSpace(cfg.ServerPassword) != ""
	cfg.PostgresPasswordSet = strings.TrimSpace(cfg.PostgresPassword) != ""
	if cfg.ServerPort <= 0 {
		cfg.ServerPort = 22
	}
	if cfg.RedisPort <= 0 {
		cfg.RedisPort = 6379
	}
	if cfg.PostgresPort <= 0 {
		cfg.PostgresPort = 5432
	}
	if strings.TrimSpace(cfg.Branch) == "" {
		cfg.Branch = "main"
	}
	if strings.TrimSpace(cfg.PostgresSSLMode) == "" {
		cfg.PostgresSSLMode = "disable"
	}
	if strings.TrimSpace(cfg.DeployCommand) == "" || isLegacyDevComposeDeployCommand(cfg.DeployCommand) {
		cfg.DeployCommand = defaultDockerImageDeployCommand
	}
	return cfg, nil
}

func (s *SettingService) SaveDeploymentSettings(ctx context.Context, cfg *DeploymentSettings) (*DeploymentSettings, error) {
	current, err := s.GetDeploymentSettings(ctx)
	if err != nil {
		return nil, err
	}
	normalized := normalizeDeploymentSettings(cfg, current)
	body, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("marshal deployment settings: %w", err)
	}
	if err := s.settingRepo.Set(ctx, SettingKeyDeploymentSettings, string(body)); err != nil {
		return nil, fmt.Errorf("save deployment settings: %w", err)
	}
	normalized.ServerPassword = ""
	normalized.PostgresPassword = ""
	normalized.RedisPassword = ""
	normalized.ServerPasswordSet = strings.TrimSpace(cfg.ServerPassword) != "" || current.ServerPasswordSet
	normalized.PostgresPasswordSet = strings.TrimSpace(cfg.PostgresPassword) != "" || current.PostgresPasswordSet
	return normalized, nil
}

func (s *SettingService) TestDeploymentEnvironment(ctx context.Context, cfg *DeploymentSettings) (*DeploymentTestResult, error) {
	current, err := s.GetDeploymentSettings(ctx)
	if err != nil {
		return nil, err
	}
	normalized := normalizeDeploymentSettings(cfg, current)
	items := make([]DeploymentCheckItem, 0, 4)

	items = append(items, testGitRepo(normalized.RepoURL))
	items = append(items, testTCP("SSH", normalized.ServerHost, normalized.ServerPort))
	items = append(items, testRedis(ctx, normalized))
	items = append(items, testPostgres(normalized))

	return &DeploymentTestResult{Items: items}, nil
}

func (s *SettingService) RunDeployment(ctx context.Context, cfg *DeploymentSettings) (*DeploymentRunResult, error) {
	current, err := s.GetDeploymentSettings(ctx)
	if err != nil {
		return nil, err
	}
	normalized := normalizeDeploymentSettings(cfg, current)
	client, err := dialSSH(normalized)
	if err != nil {
		return nil, fmt.Errorf("connect ssh: %w", err)
	}
	defer client.Close()

	command := buildDeployCommand(normalized)
	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("create ssh session: %w", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput(command)
	if err != nil {
		return &DeploymentRunResult{Output: string(output)}, fmt.Errorf("run deployment command: %w", err)
	}
	return &DeploymentRunResult{Output: string(output)}, nil
}

func defaultDeploymentSettings() *DeploymentSettings {
	return &DeploymentSettings{
		Branch:              "main",
		ServerPort:          22,
		RedisPort:           6379,
		RedisDB:             0,
		PostgresPort:        5432,
		PostgresSSLMode:     "disable",
		BackendServiceName:  "sub2api-backend",
		FrontendServiceName: "sub2api-frontend",
		TargetPath:          "/opt/sub2api",
		DeployCommand:       defaultDockerImageDeployCommand,
	}
}

func normalizeDeploymentSettings(cfg *DeploymentSettings, previous *DeploymentSettings) *DeploymentSettings {
	if cfg == nil {
		cfg = &DeploymentSettings{}
	}
	if previous == nil {
		previous = defaultDeploymentSettings()
	}
	out := *previous
	if strings.TrimSpace(cfg.RepoURL) != "" {
		out.RepoURL = strings.TrimSpace(cfg.RepoURL)
	}
	if strings.TrimSpace(cfg.Branch) != "" {
		out.Branch = strings.TrimSpace(cfg.Branch)
	}
	if strings.TrimSpace(cfg.ServerHost) != "" {
		out.ServerHost = strings.TrimSpace(cfg.ServerHost)
	}
	if cfg.ServerPort > 0 {
		out.ServerPort = cfg.ServerPort
	}
	if strings.TrimSpace(cfg.ServerUsername) != "" {
		out.ServerUsername = strings.TrimSpace(cfg.ServerUsername)
	}
	if cfg.ServerPassword != "" {
		out.ServerPassword = cfg.ServerPassword
	}
	if strings.TrimSpace(cfg.TargetPath) != "" {
		out.TargetPath = strings.TrimSpace(cfg.TargetPath)
	}
	if strings.TrimSpace(cfg.DeployCommand) != "" {
		out.DeployCommand = strings.TrimSpace(cfg.DeployCommand)
	}
	if strings.TrimSpace(cfg.BackendServiceName) != "" {
		out.BackendServiceName = strings.TrimSpace(cfg.BackendServiceName)
	}
	if strings.TrimSpace(cfg.FrontendServiceName) != "" {
		out.FrontendServiceName = strings.TrimSpace(cfg.FrontendServiceName)
	}
	if strings.TrimSpace(cfg.RedisHost) != "" {
		out.RedisHost = strings.TrimSpace(cfg.RedisHost)
	}
	if cfg.RedisPort > 0 {
		out.RedisPort = cfg.RedisPort
	}
	if cfg.RedisPassword != "" {
		out.RedisPassword = cfg.RedisPassword
	}
	out.RedisDB = cfg.RedisDB
	if strings.TrimSpace(cfg.PostgresHost) != "" {
		out.PostgresHost = strings.TrimSpace(cfg.PostgresHost)
	}
	if cfg.PostgresPort > 0 {
		out.PostgresPort = cfg.PostgresPort
	}
	if strings.TrimSpace(cfg.PostgresUser) != "" {
		out.PostgresUser = strings.TrimSpace(cfg.PostgresUser)
	}
	if cfg.PostgresPassword != "" {
		out.PostgresPassword = cfg.PostgresPassword
	}
	if strings.TrimSpace(cfg.PostgresDBName) != "" {
		out.PostgresDBName = strings.TrimSpace(cfg.PostgresDBName)
	}
	if strings.TrimSpace(cfg.PostgresSSLMode) != "" {
		out.PostgresSSLMode = strings.TrimSpace(cfg.PostgresSSLMode)
	}
	if out.ServerPort <= 0 {
		out.ServerPort = 22
	}
	if out.RedisPort <= 0 {
		out.RedisPort = 6379
	}
	if out.PostgresPort <= 0 {
		out.PostgresPort = 5432
	}
	if out.Branch == "" {
		out.Branch = "main"
	}
	if out.PostgresSSLMode == "" {
		out.PostgresSSLMode = "disable"
	}
	out.ServerPasswordSet = strings.TrimSpace(out.ServerPassword) != ""
	out.PostgresPasswordSet = strings.TrimSpace(out.PostgresPassword) != ""
	return &out
}

func testGitRepo(rawURL string) DeploymentCheckItem {
	repoURL := strings.TrimSpace(rawURL)
	if repoURL == "" {
		return DeploymentCheckItem{Name: "GitHub", OK: false, Message: "repo URL is required"}
	}
	if !strings.HasPrefix(repoURL, "http://") && !strings.HasPrefix(repoURL, "https://") {
		repoURL = "https://" + strings.TrimPrefix(repoURL, "github.com/")
	}
	if _, err := url.ParseRequestURI(repoURL); err != nil {
		return DeploymentCheckItem{Name: "GitHub", OK: false, Message: "invalid repo URL"}
	}
	return DeploymentCheckItem{Name: "GitHub", OK: true, Message: "repo URL looks valid"}
}

func testTCP(name, host string, port int) DeploymentCheckItem {
	if strings.TrimSpace(host) == "" || port <= 0 {
		return DeploymentCheckItem{Name: name, OK: false, Message: "host or port is missing"}
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)), 5*time.Second)
	if err != nil {
		return DeploymentCheckItem{Name: name, OK: false, Message: err.Error()}
	}
	_ = conn.Close()
	return DeploymentCheckItem{Name: name, OK: true, Message: "connection successful"}
}

func testRedis(ctx context.Context, cfg *DeploymentSettings) DeploymentCheckItem {
	if strings.TrimSpace(cfg.RedisHost) == "" {
		return DeploymentCheckItem{Name: "Redis", OK: false, Message: "redis host is required"}
	}
	client := redis.NewClient(&redis.Options{
		Addr:     net.JoinHostPort(cfg.RedisHost, fmt.Sprintf("%d", cfg.RedisPort)),
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	defer client.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		return DeploymentCheckItem{Name: "Redis", OK: false, Message: err.Error()}
	}
	return DeploymentCheckItem{Name: "Redis", OK: true, Message: "connection successful"}
}

func testPostgres(cfg *DeploymentSettings) DeploymentCheckItem {
	if strings.TrimSpace(cfg.PostgresHost) == "" || strings.TrimSpace(cfg.PostgresUser) == "" || strings.TrimSpace(cfg.PostgresDBName) == "" {
		return DeploymentCheckItem{Name: "PostgreSQL", OK: false, Message: "postgres config is incomplete"}
	}
	dsn := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s connect_timeout=5",
		cfg.PostgresHost,
		cfg.PostgresPort,
		cfg.PostgresUser,
		cfg.PostgresPassword,
		cfg.PostgresDBName,
		cfg.PostgresSSLMode,
	)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return DeploymentCheckItem{Name: "PostgreSQL", OK: false, Message: err.Error()}
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return DeploymentCheckItem{Name: "PostgreSQL", OK: false, Message: err.Error()}
	}
	return DeploymentCheckItem{Name: "PostgreSQL", OK: true, Message: "connection successful"}
}

func dialSSH(cfg *DeploymentSettings) (*ssh.Client, error) {
	if strings.TrimSpace(cfg.ServerHost) == "" || strings.TrimSpace(cfg.ServerUsername) == "" || strings.TrimSpace(cfg.ServerPassword) == "" {
		return nil, fmt.Errorf("server host, username and password are required")
	}
	sshConfig := &ssh.ClientConfig{
		User:            cfg.ServerUsername,
		Auth:            []ssh.AuthMethod{ssh.Password(cfg.ServerPassword)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	return ssh.Dial("tcp", net.JoinHostPort(cfg.ServerHost, fmt.Sprintf("%d", cfg.ServerPort)), sshConfig)
}

func buildDeployCommand(cfg *DeploymentSettings) string {
	if strings.TrimSpace(cfg.DeployCommand) != "" {
		return cfg.DeployCommand
	}
	return defaultDockerImageDeployCommand
}

func isLegacyDevComposeDeployCommand(command string) bool {
	normalized := strings.Join(strings.Fields(command), " ")
	return strings.Contains(normalized, "docker-compose.dev.yml") &&
		strings.Contains(normalized, "up -d --build")
}
