package main

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

const (
	distroPrefix  = "winc_"
	wslConfigPath = "etc/wsl.conf"
	wslConfig     = "# Managed by WinContainer\n[automount]\nenabled=false\nmountFsTab=false\n\n[interop]\nappendWindowsPath=false\n"
)

type ImageMetadata struct {
	Image       string   `json:"image"`
	Distro      string   `json:"distro"`
	OS          string   `json:"os"`
	Arch        string   `json:"arch"`
	Env         []string `json:"env"`
	Entrypoint  []string `json:"entrypoint"`
	Cmd         []string `json:"cmd"`
	WorkingDir  string   `json:"workingDir"`
	User        string   `json:"user"`
	Volumes     []string `json:"volumes"`
	RootFSTar   string   `json:"rootfsTar"`
	InstallPath string   `json:"installPath"`
}

type fsEntry struct {
	Header  *tar.Header
	Blob    string
	Content []byte
}

type StartOptions struct {
	Name          string
	Env           []string
	Detach        bool
	ContainerArgs []string
}

type WorkloadInfo struct {
	Name    string
	Distro  string
	Image   string
	State   string
	Volumes []string
}

type multiFlag []string

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "pull":
		if len(os.Args) < 3 {
			printUsage()
			os.Exit(1)
		}

		imageRef := os.Args[2]
		distroName := ""

		if len(os.Args) >= 4 {
			distroName = normalizeDistroName(os.Args[3])
		} else {
			distroName = defaultDistroName(imageRef)
		}

		if _, err := runPull(imageRef, distroName); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	case "import":
		if len(os.Args) < 3 {
			printUsage()
			os.Exit(1)
		}

		imageRef := os.Args[2]
		distroName := ""

		if len(os.Args) >= 4 {
			distroName = normalizeDistroName(os.Args[3])
		} else {
			distroName = defaultDistroName(imageRef)
		}

		if err := runImport(imageRef, distroName); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	case "start":
		opts, err := parseStartArgs(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

		if err := runStart(opts); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	case "list":
		if err := runList(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	case "delete", "rm":
		if err := runDelete(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	case "ps":
		if err := runPS(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	case "stats":
		if err := runStats(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("usage:")
	fmt.Println("  wincontainer pull <image> [name]")
	fmt.Println("  wincontainer import <image> [name]")
	fmt.Println("  wincontainer start <name> [-e KEY=value] [-d] [-- command args]")
	fmt.Println("  wincontainer list")
	fmt.Println("  wincontainer delete <name>")
	fmt.Println("  wincontainer ps")
	fmt.Println("  wincontainer stats <name>")
	fmt.Println()
	fmt.Println("examples:")
	fmt.Println("  wincontainer pull nginx:alpine nginx")
	fmt.Println("  wincontainer start nginx")
	fmt.Println("  wincontainer start nginx -- nginx -g \"daemon off;\"")
	fmt.Println("  wincontainer pull postgres:17-alpine postgres")
	fmt.Println("  wincontainer start postgres -e POSTGRES_PASSWORD=123456")
	fmt.Println("  wincontainer start postgres -e POSTGRES_PASSWORD=123456 -- postgres -p 5433")
	fmt.Println("  wincontainer start keycloak -d -e KC_BOOTSTRAP_ADMIN_USERNAME=admin -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin -- start-dev")
	fmt.Println("  wincontainer list")
	fmt.Println("  wincontainer delete postgres")
	fmt.Println()
	fmt.Println("networking:")
	fmt.Println("  WinContainer uses WSL localhost forwarding.")
	fmt.Println("  Keycloak receives an IPv4 JVM compatibility setting for reliable localhost access on WSL.")
	fmt.Println("  If the workload listens on 80, use localhost:80.")
	fmt.Println("  If it listens on 5432, use localhost:5432.")
	fmt.Println("  To change ports, configure the application itself.")
	fmt.Println("  No port mapping or proxy is provided.")
	fmt.Println()
	fmt.Println("volumes:")
	fmt.Println("  WinContainer detects OCI-declared volumes.")
	fmt.Println("  Declared volume paths are created inside the WSL distro.")
	fmt.Println("  Data persists inside the distro until wincontainer delete <name>.")
	fmt.Println()
	fmt.Println("detached logs:")
	fmt.Println("  start -d appends stdout and stderr to ~/.wincontainer/work/<distro>/logs/latest.log.")
	fmt.Println()
	fmt.Println("debugging:")
	fmt.Println("  Set WINCONTAINER_DEBUG=1 to print the start script with sensitive values redacted.")
	fmt.Println("  Use -e KEY to pass KEY from the current Windows environment.")
	fmt.Println()
	fmt.Println("naming:")
	fmt.Println("  Workloads are stored as WSL distros using the prefix winc_.")
	fmt.Println("  Example: name 'nginx' becomes WSL distro 'winc_nginx'.")
}

func runImport(imageRef string, distroName string) error {
	meta, err := runPull(imageRef, distroName)
	if err != nil {
		return err
	}

	return importDistro(*meta)
}

func runPull(imageRef string, distroName string) (*ImageMetadata, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	distroName = normalizeDistroName(distroName)
	safeName := sanitizeName(distroName)

	baseDir := filepath.Join(home, ".wincontainer")
	workDir := filepath.Join(baseDir, "work", safeName)
	installDir := filepath.Join(baseDir, "distros", safeName)
	rootfsTar := filepath.Join(workDir, "rootfs.tar")
	metadataPath := filepath.Join(workDir, "metadata.json")

	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(installDir, 0755); err != nil {
		return nil, err
	}

	fmt.Println("==> resolving image:", imageRef)

	ref, err := name.ParseReference(imageRef, name.WeakValidation)
	if err != nil {
		return nil, err
	}

	img, err := remote.Image(
		ref,
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
		remote.WithPlatform(v1.Platform{
			OS:           "linux",
			Architecture: "amd64",
		}),
	)
	if err != nil {
		return nil, err
	}

	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, err
	}

	meta := ImageMetadata{
		Image:       imageRef,
		Distro:      distroName,
		OS:          cfg.OS,
		Arch:        cfg.Architecture,
		Env:         cfg.Config.Env,
		Entrypoint:  cfg.Config.Entrypoint,
		Cmd:         cfg.Config.Cmd,
		WorkingDir:  cfg.Config.WorkingDir,
		User:        cfg.Config.User,
		Volumes:     extractVolumes(cfg),
		RootFSTar:   rootfsTar,
		InstallPath: installDir,
	}

	fmt.Println("==> metadata")
	fmt.Println("    distro:    ", meta.Distro)
	fmt.Println("    os:        ", meta.OS)
	fmt.Println("    arch:      ", meta.Arch)
	fmt.Println("    entrypoint:", strings.Join(meta.Entrypoint, " "))
	fmt.Println("    cmd:       ", strings.Join(meta.Cmd, " "))
	fmt.Println("    workdir:   ", meta.WorkingDir)
	fmt.Println("    user:      ", meta.User)
	fmt.Println("    volumes:   ", strings.Join(meta.Volumes, " "))

	fmt.Println("==> building rootfs.tar")
	if err := buildRootFSTar(img, rootfsTar, workDir); err != nil {
		return nil, err
	}

	if err := writeJSON(metadataPath, meta); err != nil {
		return nil, err
	}

	fmt.Println()
	fmt.Println("✅ pulled:", imageRef)
	fmt.Println("name:    ", displayNameFromDistro(meta.Distro))
	fmt.Println("distro:  ", meta.Distro)
	fmt.Println("metadata:", metadataPath)
	fmt.Println("rootfs:  ", rootfsTar)

	if len(meta.Volumes) > 0 {
		fmt.Println()
		fmt.Println("⚠️  declared persistent volumes:")
		for _, volume := range meta.Volumes {
			fmt.Println("   ", volume)
		}
		fmt.Println()
		fmt.Println("WinContainer will keep these paths inside the WSL distro filesystem.")
		fmt.Println("Data persists until you run:")
		fmt.Println("  wincontainer delete " + displayNameFromDistro(meta.Distro))
	}

	return &meta, nil
}

func extractVolumes(cfg *v1.ConfigFile) []string {
	volumes := []string{}

	for volume := range cfg.Config.Volumes {
		volume = strings.TrimSpace(volume)

		if volume == "" {
			continue
		}

		volumes = append(volumes, volume)
	}

	sort.Strings(volumes)

	return volumes
}

func runStart(opts StartOptions) error {
	meta, err := loadMetadata(opts.Name)
	if err != nil {
		return err
	}

	exists, err := distroExists(meta.Distro)
	if err != nil {
		return err
	}

	if !exists {
		fmt.Println("==> WSL distro not found, importing:", meta.Distro)
		if err := importDistro(*meta); err != nil {
			return err
		}
	}

	if err := ensureWSLRuntimeConfig(meta.Distro); err != nil {
		return err
	}

	runtimeUser, err := ensureRuntimeUser(meta.Distro, meta.User)
	if err != nil {
		return err
	}

	if err := prepareDeclaredVolumes(*meta); err != nil {
		return err
	}

	command, err := resolveStartCommand(*meta, opts)
	if err != nil {
		return err
	}

	script, err := buildStartScript(*meta, opts)
	if err != nil {
		return err
	}

	fmt.Println("==> starting:", meta.Distro)
	if len(meta.Volumes) > 0 {
		fmt.Println("==> preparing declared volumes:", strings.Join(meta.Volumes, " "))
	}
	fmt.Println("==> command:", shellJoin(command))

	if os.Getenv("WINCONTAINER_DEBUG") == "1" {
		fmt.Println("==> debug script (redacted):", redactSecrets(script))
	}

	wslArgs := []string{"-d", meta.Distro}
	if runtimeUser != "" {
		wslArgs = append(wslArgs, "-u", runtimeUser)
	}
	wslArgs = append(wslArgs, "--cd", "/", "--", "sh", "-lc", script)
	cmd := exec.Command("wsl.exe", wslArgs...)

	if opts.Detach {
		logFile, logPath, err := openDetachedLog(meta.Distro)
		if err != nil {
			return err
		}
		defer logFile.Close()

		cmd.Stdout = logFile
		cmd.Stderr = logFile
		if err := cmd.Start(); err != nil {
			return err
		}

		fmt.Println("✅ started detached")
		pid := cmd.Process.Pid
		if err := cmd.Process.Release(); err != nil {
			return err
		}

		fmt.Println("windows pid:", pid)
		fmt.Println("logs:", logPath)
		return nil
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

func openDetachedLog(distroName string) (*os.File, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", err
	}

	logDir := filepath.Join(home, ".wincontainer", "work", sanitizeName(distroName), "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, "", err
	}

	logPath := filepath.Join(logDir, "latest.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, "", err
	}

	if _, err := fmt.Fprintln(logFile, "===== wincontainer start ====="); err != nil {
		_ = logFile.Close()
		return nil, "", err
	}

	return logFile, logPath, nil
}

func ensureWSLRuntimeConfig(distroName string) error {
	currentConfig, err := exec.Command("wsl.exe", "-d", distroName, "--cd", "/", "--", "cat", "/etc/wsl.conf").Output()
	if err == nil {
		if string(currentConfig) == wslConfig || !strings.Contains(string(currentConfig), "# Managed by WinContainer") {
			return nil
		}
	}

	script := "mkdir -p /etc && " +
		"printf '%s\\n' '# Managed by WinContainer' '[automount]' 'enabled=false' 'mountFsTab=false' '' '[interop]' 'appendWindowsPath=false' > /etc/wsl.conf && " +
		"printf 'wincontainer-wsl-config-created'"

	cmd := exec.Command("wsl.exe", "-d", distroName, "--cd", "/", "--", "sh", "-lc", script)
	out, err := cmd.Output()
	if err != nil {
		return err
	}

	if !strings.Contains(string(out), "wincontainer-wsl-config-created") {
		return nil
	}

	return exec.Command("wsl.exe", "--terminate", distroName).Run()
}

func ensureRuntimeUser(distroName string, configuredUser string) (string, error) {
	configuredUser = strings.TrimSpace(configuredUser)
	if configuredUser == "" {
		return "", nil
	}

	uid, gid, err := resolveOCIUser(distroName, configuredUser)
	if err != nil {
		return "", err
	}
	if uid == "0" {
		return "", nil
	}

	const runtimeUser = "winc_runtime"
	entry := runtimeUser + ":x:" + uid + ":" + gid + ":WinContainer runtime:/:/bin/sh"
	script := "grep -Fqx " + shellQuote(entry) + " /etc/passwd 2>/dev/null || printf '%s\\n' " + shellQuote(entry) + " >> /etc/passwd; printf '%s' " + shellQuote(runtimeUser)

	out, err := exec.Command("wsl.exe", "-d", distroName, "--cd", "/", "--", "sh", "-lc", script).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("prepare OCI user %q: %w: %s", configuredUser, err, strings.TrimSpace(string(out)))
	}

	return strings.TrimSpace(string(out)), nil
}

func resolveOCIUser(distroName string, configuredUser string) (string, string, error) {
	userPart, groupPart, _ := strings.Cut(configuredUser, ":")
	passwd, err := exec.Command("wsl.exe", "-d", distroName, "--cd", "/", "--", "cat", "/etc/passwd").Output()
	if err != nil {
		return "", "", fmt.Errorf("read OCI user database: %w", err)
	}

	uid := userPart
	defaultGID := ""
	for _, line := range strings.Split(string(passwd), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 4 {
			continue
		}

		if (!isDecimal(userPart) && fields[0] == userPart) || (isDecimal(userPart) && fields[2] == userPart) {
			uid = fields[2]
			defaultGID = fields[3]
			break
		}
	}

	if !isDecimal(uid) {
		return "", "", fmt.Errorf("OCI user %q was not found in /etc/passwd", configuredUser)
	}

	if groupPart == "" {
		if !isDecimal(defaultGID) {
			return "", "", fmt.Errorf("OCI user %q has no numeric primary group", configuredUser)
		}
		return uid, defaultGID, nil
	}

	if isDecimal(groupPart) {
		return uid, groupPart, nil
	}

	groups, err := exec.Command("wsl.exe", "-d", distroName, "--cd", "/", "--", "cat", "/etc/group").Output()
	if err != nil {
		return "", "", fmt.Errorf("read OCI group database: %w", err)
	}

	for _, line := range strings.Split(string(groups), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 3 && fields[0] == groupPart && isDecimal(fields[2]) {
			return uid, fields[2], nil
		}
	}

	return "", "", fmt.Errorf("OCI group %q was not found in /etc/group", groupPart)
}

func isDecimal(value string) bool {
	if value == "" {
		return false
	}

	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}

	return true
}

func prepareDeclaredVolumes(meta ImageMetadata) error {
	commands := []string{}

	for _, volume := range meta.Volumes {
		volume = strings.TrimSpace(volume)
		if volume == "" || !strings.HasPrefix(volume, "/") {
			continue
		}

		commands = append(commands, "mkdir -p "+shellQuote(volume))
	}

	if len(commands) == 0 {
		return nil
	}

	cmd := exec.Command("wsl.exe", "-d", meta.Distro, "--cd", "/", "--", "sh", "-lc", strings.Join(commands, " && "))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("prepare declared volumes: %w", err)
	}

	return nil
}

func parseStartArgs(args []string) (StartOptions, error) {
	var opts StartOptions

	if len(args) == 0 {
		return opts, fmt.Errorf("usage: wincontainer start <name> [-e KEY=value] [-d] [-- command args]")
	}

	opts.Name = normalizeDistroName(args[0])
	raw := args[1:]

	separatorIndex := -1
	for i, arg := range raw {
		if arg == "--" {
			separatorIndex = i
			break
		}
	}

	winArgs := raw
	containerArgs := []string{}

	if separatorIndex >= 0 {
		winArgs = raw[:separatorIndex]
		containerArgs = raw[separatorIndex+1:]
	}

	fs := flag.NewFlagSet("start", flag.ContinueOnError)

	var envs multiFlag

	fs.Var(&envs, "e", "environment variable, example: KEY=value")
	fs.BoolVar(&opts.Detach, "d", false, "run detached")

	if err := fs.Parse(winArgs); err != nil {
		return opts, err
	}

	opts.Env = envs
	opts.ContainerArgs = containerArgs

	return opts, nil
}

func (m *multiFlag) String() string {
	return strings.Join(*m, ",")
}

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func runList() error {
	workloads, err := loadWorkloads()
	if err != nil {
		return err
	}

	states, err := getWslStates()
	if err != nil {
		return err
	}

	fmt.Printf("%-20s %-30s %-32s %-12s %-20s\n", "NAME", "DISTRO", "IMAGE", "STATE", "VOLUMES")
	fmt.Printf("%-20s %-30s %-32s %-12s %-20s\n", "----", "------", "-----", "-----", "-------")

	for _, workload := range workloads {
		state := states[workload.Distro]
		if state == "" {
			state = "NotImported"
		}

		volumes := "-"
		if len(workload.Volumes) > 0 {
			volumes = strings.Join(workload.Volumes, ",")
		}

		fmt.Printf(
			"%-20s %-30s %-32s %-12s %-20s\n",
			workload.Name,
			workload.Distro,
			workload.Image,
			state,
			volumes,
		)
	}

	return nil
}

func loadWorkloads() ([]WorkloadInfo, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	workRoot := filepath.Join(home, ".wincontainer", "work")

	entries, err := os.ReadDir(workRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return []WorkloadInfo{}, nil
		}
		return nil, err
	}

	workloads := []WorkloadInfo{}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		metadataPath := filepath.Join(workRoot, entry.Name(), "metadata.json")

		b, err := os.ReadFile(metadataPath)
		if err != nil {
			continue
		}

		var meta ImageMetadata
		if err := json.Unmarshal(b, &meta); err != nil {
			continue
		}

		if !strings.HasPrefix(meta.Distro, distroPrefix) {
			continue
		}

		workloads = append(workloads, WorkloadInfo{
			Name:    displayNameFromDistro(meta.Distro),
			Distro:  meta.Distro,
			Image:   meta.Image,
			Volumes: meta.Volumes,
		})
	}

	sort.Slice(workloads, func(i, j int) bool {
		return workloads[i].Distro < workloads[j].Distro
	})

	return workloads, nil
}

func getWslStates() (map[string]string, error) {
	cmd := exec.Command("wsl.exe", "--list", "--verbose")

	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	text := strings.ReplaceAll(string(out), "\x00", "")
	states := map[string]string{}

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)

		if line == "" {
			continue
		}

		line = strings.TrimPrefix(line, "*")
		line = strings.TrimSpace(line)

		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		name := fields[0]
		if name == "NAME" {
			continue
		}

		if !strings.HasPrefix(name, distroPrefix) {
			continue
		}

		state := fields[1]
		states[name] = state
	}

	return states, nil
}

func runDelete(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: wincontainer delete <name>")
	}

	distroName := normalizeDistroName(args[0])

	fmt.Println("==> deleting:", distroName)

	exists, err := distroExists(distroName)
	if err != nil {
		return err
	}

	if exists {
		fmt.Println("==> unregistering WSL distro:", distroName)

		cmd := exec.Command("wsl.exe", "--unregister", distroName)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin

		if err := cmd.Run(); err != nil {
			return err
		}
	} else {
		fmt.Println("==> WSL distro not found, skipping unregister")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	safeName := sanitizeName(distroName)

	workDir := filepath.Join(home, ".wincontainer", "work", safeName)
	installDir := filepath.Join(home, ".wincontainer", "distros", safeName)

	if err := os.RemoveAll(workDir); err != nil {
		return err
	}

	if err := os.RemoveAll(installDir); err != nil {
		return err
	}

	fmt.Println("✅ deleted:", displayNameFromDistro(distroName))

	return nil
}

func buildRootFSTar(img v1.Image, outTar string, workDir string) error {
	layers, err := img.Layers()
	if err != nil {
		return err
	}

	blobDir := filepath.Join(workDir, "blobs")
	_ = os.RemoveAll(blobDir)

	if err := os.MkdirAll(blobDir, 0755); err != nil {
		return err
	}

	entries := map[string]*fsEntry{}

	for i, layer := range layers {
		fmt.Printf("    applying layer %d/%d\n", i+1, len(layers))

		rc, err := layer.Uncompressed()
		if err != nil {
			return err
		}

		err = applyLayer(rc, entries, blobDir)
		closeErr := rc.Close()

		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}

	return writeMergedTar(outTar, entries)
}

func applyLayer(r io.Reader, entries map[string]*fsEntry, blobDir string) error {
	tr := tar.NewReader(r)

	for {
		hdr, err := tr.Next()

		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return err
		}

		cleanName, ok := cleanTarName(hdr.Name)
		if !ok {
			continue
		}

		base := path.Base(cleanName)
		dir := path.Dir(cleanName)
		if dir == "." {
			dir = ""
		}

		if base == ".wh..wh..opq" {
			removeChildren(entries, dir)
			continue
		}

		if strings.HasPrefix(base, ".wh.") {
			targetBase := strings.TrimPrefix(base, ".wh.")
			target := path.Join(dir, targetBase)
			removePath(entries, target)
			continue
		}

		h := *hdr
		h.Name = cleanName

		if hdr.PAXRecords != nil {
			h.PAXRecords = map[string]string{}
			for k, v := range hdr.PAXRecords {
				h.PAXRecords[k] = v
			}
		}

		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			tmp, err := os.CreateTemp(blobDir, "blob-*")
			if err != nil {
				return err
			}

			if _, err := io.Copy(tmp, tr); err != nil {
				_ = tmp.Close()
				return err
			}

			if err := tmp.Close(); err != nil {
				return err
			}

			entries[cleanName] = &fsEntry{
				Header: &h,
				Blob:   tmp.Name(),
			}

		default:
			entries[cleanName] = &fsEntry{
				Header: &h,
			}
		}
	}

	return nil
}

func writeMergedTar(outTar string, entries map[string]*fsEntry) error {
	injectWSLRuntimeConfig(entries)

	f, err := os.Create(outTar)
	if err != nil {
		return err
	}
	defer f.Close()

	tw := tar.NewWriter(f)
	defer tw.Close()

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}

	sort.Slice(names, func(i, j int) bool {
		a := entries[names[i]].Header
		b := entries[names[j]].Header

		if a.Typeflag == tar.TypeDir && b.Typeflag != tar.TypeDir {
			return true
		}

		if a.Typeflag != tar.TypeDir && b.Typeflag == tar.TypeDir {
			return false
		}

		return names[i] < names[j]
	})

	for _, name := range names {
		entry := entries[name]
		h := *entry.Header
		h.Name = name

		if err := tw.WriteHeader(&h); err != nil {
			return err
		}

		if entry.Blob != "" {
			blob, err := os.Open(entry.Blob)
			if err != nil {
				return err
			}

			_, copyErr := io.Copy(tw, blob)
			closeErr := blob.Close()

			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}

		if entry.Content != nil {
			if _, err := tw.Write(entry.Content); err != nil {
				return err
			}
		}
	}

	return nil
}

func injectWSLRuntimeConfig(entries map[string]*fsEntry) {
	if _, ok := entries["etc"]; !ok {
		entries["etc"] = &fsEntry{
			Header: &tar.Header{
				Name:     "etc",
				Mode:     0755,
				Typeflag: tar.TypeDir,
			},
		}
	}

	entries[wslConfigPath] = &fsEntry{
		Header: &tar.Header{
			Name:     wslConfigPath,
			Mode:     0644,
			Size:     int64(len(wslConfig)),
			Typeflag: tar.TypeReg,
		},
		Content: []byte(wslConfig),
	}
}

func loadMetadata(name string) (*ImageMetadata, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	distroName := normalizeDistroName(name)
	safeName := sanitizeName(distroName)
	metadataPath := filepath.Join(home, ".wincontainer", "work", safeName, "metadata.json")

	b, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("metadata not found for %s. Did you run wincontainer pull first? %w", distroName, err)
	}

	var meta ImageMetadata
	if err := json.Unmarshal(b, &meta); err != nil {
		return nil, err
	}

	return &meta, nil
}

func importDistro(meta ImageMetadata) error {
	exists, err := distroExists(meta.Distro)
	if err != nil {
		return err
	}

	if exists {
		fmt.Println("==> WSL distro already exists:", meta.Distro)
		return nil
	}

	if err := os.MkdirAll(meta.InstallPath, 0755); err != nil {
		return err
	}

	fmt.Println("==> importing into WSL:", meta.Distro)

	cmd := exec.Command(
		"wsl.exe",
		"--import",
		meta.Distro,
		meta.InstallPath,
		meta.RootFSTar,
		"--version",
		"2",
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

func distroExists(distroName string) (bool, error) {
	cmd := exec.Command("wsl.exe", "--list", "--quiet")

	out, err := cmd.Output()
	if err != nil {
		return false, err
	}

	text := strings.ReplaceAll(string(out), "\x00", "")

	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == distroName {
			return true, nil
		}
	}

	return false, nil
}

func buildStartScript(meta ImageMetadata, opts StartOptions) (string, error) {
	lines := []string{}

	lines = append(lines, "unset LANG LC_ALL LANGUAGE || true")

	allEnv, err := buildStartEnvironment(meta, opts)
	if err != nil {
		return "", err
	}

	processEnv := []string{}

	for _, kv := range allEnv {
		key, value, hasValue, err := parseEnvironment(kv)
		if err != nil {
			return "", err
		}
		if !hasValue {
			continue
		}

		if isShellEnvironmentKey(key) {
			lines = append(lines, fmt.Sprintf("export %s=%s", key, shellQuote(value)))
		} else {
			processEnv = append(processEnv, key+"="+value)
		}
	}

	if isRabbitMQWorkload(meta) {
		lines = append(lines, ensureHostAliasScript(workloadHostname(meta.Distro)))
	}

	if meta.WorkingDir != "" {
		lines = append(lines, "cd "+shellQuote(meta.WorkingDir))
	}

	command, err := resolveStartCommand(meta, opts)
	if err != nil {
		return "", err
	}

	execCommand := "exec "
	if len(processEnv) > 0 {
		execCommand += "env " + shellJoin(processEnv) + " "
	}
	lines = append(lines, execCommand+shellJoin(command))

	return strings.Join(lines, " && "), nil
}

func buildStartEnvironment(meta ImageMetadata, opts StartOptions) ([]string, error) {
	allEnv := append([]string{}, meta.Env...)

	for _, raw := range opts.Env {
		key, value, hasValue, err := parseEnvironment(raw)
		if err != nil {
			return nil, err
		}

		if !hasValue {
			value, _ = os.LookupEnv(key)
		}

		allEnv = append(allEnv, key+"="+value)
	}

	if isKeycloakWorkload(meta) && !environmentContainsKey(allEnv, "JAVA_OPTS_APPEND") {
		allEnv = append(allEnv, "JAVA_OPTS_APPEND=-Djava.net.preferIPv4Stack=true")
	}

	if isRabbitMQWorkload(meta) && !environmentContainsKey(allEnv, "RABBITMQ_NODENAME") {
		allEnv = append(allEnv, "RABBITMQ_NODENAME=rabbit@"+workloadHostname(meta.Distro))
	}

	return allEnv, nil
}

func parseEnvironment(raw string) (key string, value string, hasValue bool, err error) {
	parts := strings.SplitN(raw, "=", 2)
	key = strings.TrimSpace(parts[0])
	if !isEnvironmentKey(key) {
		return "", "", false, fmt.Errorf("invalid environment variable %q", raw)
	}

	if len(parts) == 2 {
		return key, parts[1], true, nil
	}

	return key, "", false, nil
}

func isEnvironmentKey(key string) bool {
	if key == "" {
		return false
	}

	for i := 0; i < len(key); i++ {
		c := key[i]
		if c == '=' || c == 0 || c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			return false
		}
	}

	return true
}

func isShellEnvironmentKey(key string) bool {
	if key == "" {
		return false
	}

	for i := 0; i < len(key); i++ {
		c := key[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_' {
			continue
		}
		if i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}

	return true
}

func environmentContainsKey(env []string, wanted string) bool {
	for _, raw := range env {
		key, _, _, err := parseEnvironment(raw)
		if err == nil && key == wanted {
			return true
		}
	}

	return false
}

func isRabbitMQWorkload(meta ImageMetadata) bool {
	return strings.Contains(strings.ToLower(meta.Image), "rabbitmq")
}

func isKeycloakWorkload(meta ImageMetadata) bool {
	return strings.Contains(strings.ToLower(meta.Image), "keycloak")
}

func workloadHostname(distroName string) string {
	hostname := strings.TrimPrefix(distroName, distroPrefix)
	hostname = strings.ReplaceAll(hostname, "_", "-")
	hostname = strings.ReplaceAll(hostname, ".", "-")

	if hostname == "" {
		return "wincontainer"
	}

	return hostname
}

func ensureHostAliasScript(hostname string) string {
	entry := "127.0.0.1 " + hostname
	return "grep -Fqx " + shellQuote(entry) + " /etc/hosts 2>/dev/null || printf '%s\\n' " + shellQuote(entry) + " >> /etc/hosts"
}

func resolveStartCommand(meta ImageMetadata, opts StartOptions) ([]string, error) {
	command := append([]string{}, meta.Entrypoint...)
	if len(opts.ContainerArgs) > 0 {
		command = append(command, opts.ContainerArgs...)
	} else {
		command = append(command, meta.Cmd...)
	}

	if len(command) == 0 {
		return nil, fmt.Errorf("no command found. Image has no ENTRYPOINT/CMD, pass command after --")
	}

	return command, nil
}

func shellJoin(args []string) string {
	quoted := make([]string, 0, len(args))

	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}

	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func redactSecrets(value string) string {
	parts := strings.Split(value, " && ")

	for i, part := range parts {
		part = redactExecEnvSecrets(part)
		parts[i] = part

		trimmed := strings.TrimSpace(part)
		assignmentStart := 0

		if strings.HasPrefix(trimmed, "export ") {
			assignmentStart = len("export ")
		}

		assignment := trimmed[assignmentStart:]
		equals := strings.Index(assignment, "=")
		if equals <= 0 {
			continue
		}

		key := strings.TrimSpace(assignment[:equals])
		if !isSensitiveKey(key) {
			continue
		}

		leading := part[:len(part)-len(strings.TrimLeft(part, " \t"))]
		parts[i] = leading + trimmed[:assignmentStart+equals+1] + shellQuote("***")
		parts[i] = redactExecEnvSecrets(parts[i])
	}

	return strings.Join(parts, " && ")
}

func redactExecEnvSecrets(value string) string {
	const prefix = "exec env "
	start := strings.Index(value, prefix)
	if start < 0 {
		return value
	}

	type replacement struct {
		start int
		end   int
		value string
	}

	replacements := []replacement{}
	pos := start + len(prefix)

	for pos < len(value) {
		for pos < len(value) && (value[pos] == ' ' || value[pos] == '\t') {
			pos++
		}
		if pos >= len(value) {
			break
		}

		end := shellTokenEnd(value, pos)
		if end <= pos {
			break
		}

		token := value[pos:end]
		keyStart := 0
		if strings.HasPrefix(token, "'") {
			keyStart = 1
		}

		equals := strings.Index(token[keyStart:], "=")
		if equals < 0 {
			break
		}
		equals += keyStart

		key := token[keyStart:equals]
		if !isEnvironmentKey(key) {
			break
		}

		if isSensitiveKey(key) {
			replacements = append(replacements, replacement{
				start: pos,
				end:   end,
				value: shellQuote(key + "=***"),
			})
		}

		pos = end
	}

	for i := len(replacements) - 1; i >= 0; i-- {
		replacement := replacements[i]
		value = value[:replacement.start] + replacement.value + value[replacement.end:]
	}

	return value
}

func shellTokenEnd(value string, start int) int {
	inSingleQuote := false
	inDoubleQuote := false

	for i := start; i < len(value); i++ {
		switch value[i] {
		case '\\':
			if !inSingleQuote && i+1 < len(value) {
				i++
			}
		case '\'':
			if !inDoubleQuote {
				inSingleQuote = !inSingleQuote
			}
		case '"':
			if !inSingleQuote {
				inDoubleQuote = !inDoubleQuote
			}
		case ' ', '\t':
			if !inSingleQuote && !inDoubleQuote {
				return i
			}
		}
	}

	return len(value)
}

func isSensitiveKey(key string) bool {
	upperKey := strings.ToUpper(key)

	for _, marker := range []string{"PASS", "PASSWORD", "SECRET", "TOKEN", "CREDENTIAL", "PRIVATE"} {
		if strings.Contains(upperKey, marker) {
			return true
		}
	}

	return false
}

func runPS() error {
	cmd := exec.Command("wsl.exe", "--list", "--verbose")

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

func runStats(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: wincontainer stats <name>")
	}

	distroName := normalizeDistroName(args[0])

	script := `
total=0
for f in /proc/[0-9]*/status; do
  [ -r "$f" ] || continue
  name=
  rss=
  while read -r key value _; do
    case "$key" in
      Name:) name=$value ;;
      VmRSS:) rss=$value ;;
    esac
  done < "$f"

  case "$rss" in
    ''|*[!0-9]*) continue ;;
  esac

  total=$((total + rss))
  mb_whole=$((rss / 1024))
  mb_fraction=$(((rss % 1024) * 100 / 1024))
  printf "%-24s %8d.%02d MB\n" "$name" "$mb_whole" "$mb_fraction"
done
total_mb_whole=$((total / 1024))
total_mb_fraction=$(((total % 1024) * 100 / 1024))
printf "\n%-24s %8d.%02d MB\n" "TOTAL" "$total_mb_whole" "$total_mb_fraction"
`

	cmd := exec.Command(
		"wsl.exe",
		"-d",
		distroName,
		"--cd",
		"/",
		"--",
		"sh",
		"-lc",
		script,
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

func removePath(entries map[string]*fsEntry, target string) {
	target = strings.TrimPrefix(path.Clean("/"+target), "/")
	if target == "." || target == "" {
		return
	}

	for name := range entries {
		if name == target || strings.HasPrefix(name, target+"/") {
			delete(entries, name)
		}
	}
}

func removeChildren(entries map[string]*fsEntry, dir string) {
	dir = strings.TrimPrefix(path.Clean("/"+dir), "/")
	if dir == "." {
		dir = ""
	}

	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}

	for name := range entries {
		if strings.HasPrefix(name, prefix) && name != dir {
			delete(entries, name)
		}
	}
}

func cleanTarName(name string) (string, bool) {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimPrefix(name, "/")
	name = path.Clean("/" + name)
	name = strings.TrimPrefix(name, "/")

	if name == "." || name == "" {
		return "", false
	}

	if strings.HasPrefix(name, "../") {
		return "", false
	}

	return name, true
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, b, 0644)
}

func sanitizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))

	replacer := strings.NewReplacer(
		"/", "-",
		":", "-",
		"@", "-",
		"\\", "-",
		".", "-",
		" ", "-",
	)

	return replacer.Replace(s)
}

func defaultDistroName(imageRef string) string {
	return normalizeDistroName(imageRef)
}

func normalizeDistroName(name string) string {
	safe := sanitizeName(name)

	if strings.HasPrefix(safe, distroPrefix) {
		return safe
	}

	return distroPrefix + safe
}

func displayNameFromDistro(distroName string) string {
	return strings.TrimPrefix(distroName, distroPrefix)
}
