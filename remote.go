package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const defaultSSHPort = "22"

type remoteClient interface {
	Run(ctx context.Context, command string) error
	Close() error
}

type remoteStdinClient interface {
	RunWithStdin(context.Context, string, io.Reader) error
}

type sshRemoteClient struct {
	client *ssh.Client
	stdout io.Writer
	stderr io.Writer
}

func newSSHRemoteClient(ctx context.Context, host, user, privateKeyPath string, stdout, stderr io.Writer) (*sshRemoteClient, error) {
	hostPort, err := sshHostPort(host)
	if err != nil {
		return nil, err
	}
	key, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	config := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: acceptNewHostKeyCallback(),
		Timeout:         30 * time.Second,
	}

	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", hostPort, err)
	}
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, hostPort, config)
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("start SSH session: %w", err)
	}
	return &sshRemoteClient{
		client: ssh.NewClient(clientConnection, channels, requests),
		stdout: stdout,
		stderr: stderr,
	}, nil
}

func (client *sshRemoteClient) Run(ctx context.Context, command string) error {
	return client.RunWithStdin(ctx, command, nil)
}

func (client *sshRemoteClient) RunWithStdin(ctx context.Context, command string, stdin io.Reader) error {
	session, err := client.client.NewSession()
	if err != nil {
		return fmt.Errorf("create SSH session: %w", err)
	}
	defer session.Close()
	session.Stdout = client.stdout
	session.Stderr = client.stderr
	session.Stdin = stdin

	done := make(chan error, 1)
	go func() {
		done <- session.Run(command)
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("remote command failed: %w", err)
		}
		return nil
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return ctx.Err()
	}
}

func (client *sshRemoteClient) Close() error {
	return client.client.Close()
}

func sshHostPort(host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", errors.New("host is required")
	}
	if strings.Contains(host, "://") {
		return "", fmt.Errorf("host must not include a URL scheme: %s", host)
	}
	if parsedHost, parsedPort, err := net.SplitHostPort(host); err == nil {
		if parsedHost == "" || parsedPort == "" {
			return "", fmt.Errorf("invalid SSH host: %s", host)
		}
		if err := validatePort(parsedPort); err != nil {
			return "", err
		}
		return net.JoinHostPort(parsedHost, parsedPort), nil
	}
	if strings.Count(host, ":") == 1 {
		parsedHost, parsedPort, ok := strings.Cut(host, ":")
		if ok && parsedHost != "" && parsedPort != "" {
			if err := validatePort(parsedPort); err != nil {
				return "", err
			}
			return net.JoinHostPort(parsedHost, parsedPort), nil
		}
	}
	return net.JoinHostPort(host, defaultSSHPort), nil
}

func acceptNewHostKeyCallback() ssh.HostKeyCallback {
	path := knownHostsPath()
	callback, err := knownhosts.New(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			return fmt.Errorf("load known hosts: %w", err)
		}
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if callback != nil {
			if err := callback(hostname, remote, key); err == nil {
				return nil
			} else if !isUnknownHostKey(err) {
				return err
			}
		}
		return appendKnownHost(path, hostname, key)
	}
}

func knownHostsPath() string {
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".ssh", "known_hosts")
	}
	return filepath.Join(".ssh", "known_hosts")
}

func isUnknownHostKey(err error) bool {
	var keyError *knownhosts.KeyError
	return errors.As(err, &keyError) && len(keyError.Want) == 0
}

func appendKnownHost(path, hostname string, key ssh.PublicKey) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create known hosts directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open known hosts file: %w", err)
	}
	defer file.Close()
	if _, err := fmt.Fprintln(file, knownhosts.Line([]string{hostname}, key)); err != nil {
		return fmt.Errorf("write known host: %w", err)
	}
	return nil
}

func remoteWriteFileCommand(path, content, owner, group string, mode os.FileMode) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	temporaryPath := path + ".servestead.tmp"
	return strings.Join([]string{
		"set -e",
		"mkdir -p " + shellQuote(filepath.Dir(path)),
		"base64 -d > " + shellQuote(temporaryPath) + " <<'SERVESTEAD_FILE'",
		encoded,
		"SERVESTEAD_FILE",
		"chown " + shellQuote(owner+":"+group) + " " + shellQuote(temporaryPath),
		"chmod " + shellQuote(fileModeDigits(mode)) + " " + shellQuote(temporaryPath),
		"mv " + shellQuote(temporaryPath) + " " + shellQuote(path),
	}, "\n")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func fileModeDigits(mode os.FileMode) string {
	return fmt.Sprintf("%04o", mode.Perm())
}

func commandScript(lines ...string) string {
	var builder bytes.Buffer
	builder.WriteString("set -e\n")
	for _, line := range lines {
		builder.WriteString(line)
		builder.WriteByte('\n')
	}
	return builder.String()
}

func privilegedCommand(sshUser, script string) string {
	if sshUser == "root" {
		return "sh -c " + shellQuote(script)
	}
	return "sudo sh -c " + shellQuote(script)
}

func aptInstallCommand(packages ...string) string {
	quoted := make([]string, 0, len(packages))
	for _, pkg := range packages {
		quoted = append(quoted, shellQuote(pkg))
	}
	return noninteractiveAptGetCommand("update") + " && " + noninteractiveAptGetCommand("install -y "+strings.Join(quoted, " "))
}

func aptGetCommand(command string) string {
	return "apt-get -o DPkg::Lock::Timeout=300 " + command
}

func noninteractiveAptGetCommand(command string) string {
	return "DEBIAN_FRONTEND=noninteractive " + aptGetCommand(command)
}

func systemctlCommand(action, service string) string {
	return "systemctl " + shellQuote(action) + " " + shellQuote(service)
}

func validatePort(port string) error {
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return fmt.Errorf("invalid SSH port: %s", port)
	}
	return nil
}
