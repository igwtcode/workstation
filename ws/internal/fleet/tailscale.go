package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
)

type Node struct {
	Name        string   `json:"name"`
	DNSName     string   `json:"dnsName,omitempty"`
	TailscaleIP string   `json:"tailscaleIp,omitempty"`
	OS          string   `json:"os,omitempty"`
	Arch        string   `json:"arch,omitempty"`
	Online      bool     `json:"online"`
	SSHEnabled  bool     `json:"sshEnabled"`
	IsSelf      bool     `json:"isSelf,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Provider    string   `json:"provider,omitempty"`
	TargetID    string   `json:"targetId,omitempty"`
	LastSeen    string   `json:"lastSeen,omitempty"`
}

type TailscaleRunner interface {
	Output(name string, args ...string) ([]byte, error)
}

type JoinOptions struct {
	AuthKeyEnv string   `json:"authKeyEnv,omitempty"`
	Hostname   string   `json:"hostname,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	SSH        bool     `json:"ssh"`
}

const tailscaleProbeTimeout = 15 * time.Second

type execTailscaleRunner struct{}

func (execTailscaleRunner) Output(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tailscaleProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, fmt.Errorf("%s timed out after %s: %w", name, tailscaleProbeTimeout, ctx.Err())
	}
	return out, err
}

var (
	hostnameLabelRe = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
	nodeTargetRe    = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9.-]{0,251}[a-zA-Z0-9])?$`)
	sshUserRe       = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,64}$`)
	tagRe           = regexp.MustCompile(`^tag:[a-zA-Z][a-zA-Z0-9-]{0,62}$`)
	envVarNameRe    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
	presetIDRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	safeTokenArgRe  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
)

func ValidateHostname(name string) error {
	if !hostnameLabelRe.MatchString(name) {
		return fmt.Errorf("invalid hostname %q (must be 1-63 alphanumeric or hyphen chars)", name)
	}
	return nil
}

func ValidateNodeTarget(target string) error {
	if target == "" {
		return fmt.Errorf("node target is required")
	}
	host := target
	if user, h, hasAt := strings.Cut(target, "@"); hasAt {
		if !sshUserRe.MatchString(user) {
			return fmt.Errorf("invalid node SSH user %q", user)
		}
		host = h
	}
	if host == "" {
		return fmt.Errorf("node target host is required")
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	if strings.Contains(host, "..") || !nodeTargetRe.MatchString(host) {
		return fmt.Errorf("invalid node name %q", target)
	}
	return nil
}

func ValidateTag(tag string) error {
	if !tagRe.MatchString(tag) {
		return fmt.Errorf("invalid tailscale tag %q (expected tag:<name>)", tag)
	}
	return nil
}

func ValidateEnvVarName(name string) error {
	if !envVarNameRe.MatchString(name) {
		return fmt.Errorf("invalid environment variable name %q", name)
	}
	return nil
}

func ValidatePresetID(preset string) error {
	if !presetIDRe.MatchString(preset) {
		return fmt.Errorf("invalid preset ID %q", preset)
	}
	return nil
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type tsStatusDoc struct {
	Self *tsPeerDoc            `json:"Self"`
	Peer map[string]*tsPeerDoc `json:"Peer"`
}

type tsPeerDoc struct {
	HostName     string   `json:"HostName"`
	DNSName      string   `json:"DNSName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	OS           string   `json:"OS"`
	Arch         string   `json:"Arch"`
	Online       bool     `json:"Online"`
	Tags         []string `json:"Tags"`
	Capabilities []string `json:"Capabilities"`
	SSHHostKeys  []string `json:"sshHostKeys"`
	LastSeen     string   `json:"LastSeen"`
}

func ListTailscaleNodes(ctx *app.Context, runner TailscaleRunner) ([]Node, error) {
	_ = ctx
	if runner == nil {
		if _, err := exec.LookPath("tailscale"); err != nil {
			return nil, fmt.Errorf("tailscale CLI not found on PATH: install Tailscale to list fleet nodes")
		}
		runner = execTailscaleRunner{}
	}

	out, err := runner.Output("tailscale", "status", "--json")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("tailscale CLI not found on PATH: install Tailscale to list fleet nodes")
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("tailscale status --json failed: %w (%s)", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("tailscale status --json failed: %w", err)
	}

	var doc tsStatusDoc
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("parse tailscale status JSON: %w", err)
	}

	var nodes []Node
	if doc.Self != nil {
		nodes = append(nodes, peerToNode(doc.Self, true))
	}
	for _, p := range doc.Peer {
		if p == nil {
			continue
		}
		nodes = append(nodes, peerToNode(p, false))
	}

	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].IsSelf != nodes[j].IsSelf {
			return nodes[i].IsSelf
		}
		if nodes[i].Online != nodes[j].Online {
			return nodes[i].Online
		}
		return nodes[i].Name < nodes[j].Name
	})

	return nodes, nil
}

func peerToNode(p *tsPeerDoc, isSelf bool) Node {
	dns := strings.TrimSuffix(strings.TrimSpace(p.DNSName), ".")
	name := ""
	if dns != "" {
		name, _, _ = strings.Cut(dns, ".")
	}
	if name == "" {
		name = strings.TrimSpace(p.HostName)
	}

	ip := pickIPv4(p.TailscaleIPs)
	sshEnabled := len(p.SSHHostKeys) > 0
	if !sshEnabled {
		for _, capStr := range p.Capabilities {
			if strings.Contains(strings.ToLower(capStr), "ssh") {
				sshEnabled = true
				break
			}
		}
	}

	tags := make([]string, len(p.Tags))
	copy(tags, p.Tags)
	sort.Strings(tags)

	provider := "tailscale"
	for _, t := range tags {
		lower := strings.ToLower(t)
		if strings.Contains(lower, "hetzner") {
			provider = "hetzner"
			break
		}
		if strings.Contains(lower, "aws") || strings.Contains(lower, "ec2") {
			provider = "aws"
			break
		}
	}

	targetID := ""
	if dns != "" && ValidateNodeTarget(dns) == nil {
		targetID = dns
	} else if dns != "" {
		if lbl, _, _ := strings.Cut(dns, "."); lbl != "" && ValidateNodeTarget(lbl) == nil {
			targetID = lbl
		}
	}
	if targetID == "" && ip != "" {
		targetID = ip
	}
	if targetID == "" && ValidateNodeTarget(name) == nil {
		targetID = name
	}

	return Node{
		Name:        name,
		DNSName:     dns,
		TailscaleIP: ip,
		OS:          strings.ToLower(strings.TrimSpace(p.OS)),
		Arch:        strings.ToLower(strings.TrimSpace(p.Arch)),
		Online:      p.Online,
		SSHEnabled:  sshEnabled,
		IsSelf:      isSelf,
		Tags:        tags,
		Provider:    provider,
		TargetID:    targetID,
		LastSeen:    p.LastSeen,
	}
}

func pickIPv4(ips []string) string {
	for _, ip := range ips {
		if !strings.Contains(ip, ":") {
			return ip
		}
	}
	if len(ips) > 0 {
		return ips[0]
	}
	return ""
}

func RenderJoinCommand(opts JoinOptions) (string, error) {
	if opts.Hostname != "" {
		if err := ValidateHostname(opts.Hostname); err != nil {
			return "", err
		}
	}
	if opts.AuthKeyEnv != "" {
		if err := ValidateEnvVarName(opts.AuthKeyEnv); err != nil {
			return "", err
		}
	}
	cleanTags, err := validateTags(opts.Tags)
	if err != nil {
		return "", err
	}

	parts := []string{"tailscale", "up"}
	if opts.SSH {
		parts = append(parts, "--ssh")
	}
	if opts.AuthKeyEnv != "" {
		parts = append(parts, fmt.Sprintf(`--authkey="$%s"`, opts.AuthKeyEnv))
	}
	if opts.Hostname != "" {
		parts = append(parts, "--hostname="+opts.Hostname)
	}
	if len(cleanTags) > 0 {
		parts = append(parts, "--advertise-tags="+strings.Join(cleanTags, ","))
	}
	return strings.Join(parts, " "), nil
}

func RenderTagsCommand(tags []string) (string, error) {
	cleanTags, err := validateTags(tags)
	if err != nil {
		return "", err
	}
	if len(cleanTags) == 0 {
		return "", fmt.Errorf("at least one tag is required")
	}
	return "tailscale set --advertise-tags=" + strings.Join(cleanTags, ","), nil
}

func validateTags(tags []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if err := ValidateTag(t); err != nil {
			return nil, err
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, nil
}
