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
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

const (
	distroPrefix  = "winc_"
	wslConfigPath = "etc/wsl.conf"
	wslConfig     = "# Managed by WinContainer\n[automount]\nenabled=false\nmountFsTab=false\n\n[interop]\nappendWindowsPath=false\n"
)

type ImageMetadata struct {
	Image        string   `json:"image"`
	Distro       string   `json:"distro"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Env          []string `json:"env"`
	Entrypoint   []string `json:"entrypoint"`
	Cmd          []string `json:"cmd"`
	WorkingDir   string   `json:"workingDir"`
	User         string   `json:"user"`
	Volumes      []string `json:"volumes"`
	ExposedPorts []string `json:"exposedPorts,omitempty"`
	RootFSTar    string   `json:"rootfsTar"`
	InstallPath  string   `json:"installPath"`
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

type BuildOptions struct {
	Tag            string
	DockerfilePath string
	ContextDir     string
}

type DockerInstruction struct {
	Op   string
	Args string
	Line int
	Raw  string
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "build":
		opts, err := parseBuildArgs(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

		if err := runBuild(opts); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	case "push":
		if err := runPush(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

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
	fmt.Println("  wincontainer build -t <name> [-f Dockerfile] <context>")
	fmt.Println("  wincontainer push <name> <target-ref>")
	fmt.Println("  wincontainer pull <image> [name]")
	fmt.Println("  wincontainer import <image> [name]")
	fmt.Println("  wincontainer start <name> [-e KEY=value] [-d] [-- command args]")
	fmt.Println("  wincontainer list")
	fmt.Println("  wincontainer delete <name>")
	fmt.Println("  wincontainer ps")
	fmt.Println("  wincontainer stats <name>")
	fmt.Println()
	fmt.Println("examples:")
	fmt.Println("  wincontainer build -t my-app .")
	fmt.Println("  wincontainer push my-app ghcr.io/user/my-app:latest")
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

func parseBuildArgs(args []string) (BuildOptions, error) {
	var opts BuildOptions

	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.StringVar(&opts.Tag, "t", "", "local image/workload name")
	fs.StringVar(&opts.DockerfilePath, "f", "Dockerfile", "Dockerfile path")

	if err := fs.Parse(args); err != nil {
		return opts, err
	}

	if opts.Tag == "" {
		return opts, fmt.Errorf("usage: wincontainer build -t <name> [-f Dockerfile] <context>")
	}

	remaining := fs.Args()
	if len(remaining) != 1 {
		return opts, fmt.Errorf("usage: wincontainer build -t <name> [-f Dockerfile] <context>")
	}

	contextDir, err := filepath.Abs(remaining[0])
	if err != nil {
		return opts, err
	}

	if info, err := os.Stat(contextDir); err != nil {
		return opts, err
	} else if !info.IsDir() {
		return opts, fmt.Errorf("build context is not a directory: %s", contextDir)
	}

	dockerfilePath := opts.DockerfilePath
	if !filepath.IsAbs(dockerfilePath) {
		dockerfilePath = filepath.Join(contextDir, dockerfilePath)
	}

	opts.ContextDir = contextDir
	opts.DockerfilePath = dockerfilePath
	opts.Tag = displayNameFromDistro(normalizeDistroName(opts.Tag))

	return opts, nil
}

func runBuild(opts BuildOptions) error {
	instructions, err := parseDockerfile(opts.DockerfilePath)
	if err != nil {
		return err
	}

	if len(instructions) == 0 || instructions[0].Op != "FROM" {
		return fmt.Errorf("Dockerfile must start with FROM")
	}

	baseImage, err := parseFromImage(instructions[0].Args)
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	finalDistro := normalizeDistroName(opts.Tag)
	finalSafe := sanitizeName(finalDistro)
	finalWorkDir := filepath.Join(home, ".wincontainer", "work", finalSafe)
	finalInstallDir := filepath.Join(home, ".wincontainer", "distros", finalSafe)
	finalRootFSTar := filepath.Join(finalWorkDir, "rootfs.tar")
	finalMetadataPath := filepath.Join(finalWorkDir, "metadata.json")

	buildID := fmt.Sprintf("%d", time.Now().UnixNano())
	builderDistro := normalizeDistroName("build-" + sanitizeName(opts.Tag) + "-" + buildID)
	builderSafe := sanitizeName(builderDistro)

	fmt.Println("==> parsing Dockerfile:", opts.DockerfilePath)
	fmt.Println("==> build context:", opts.ContextDir)
	fmt.Println("==> resolving base image:", baseImage)

	baseMeta, err := runPull(baseImage, builderDistro)
	if err != nil {
		return err
	}

	cleanupBuilder := func() {
		_ = exec.Command("wsl.exe", "--terminate", builderDistro).Run()
		_ = exec.Command("wsl.exe", "--unregister", builderDistro).Run()
		_ = os.RemoveAll(filepath.Join(home, ".wincontainer", "work", builderSafe))
		_ = os.RemoveAll(filepath.Join(home, ".wincontainer", "distros", builderSafe))
	}
	defer cleanupBuilder()

	fmt.Println("==> importing builder distro:", builderDistro)
	if err := importDistro(*baseMeta); err != nil {
		return err
	}

	if err := ensureWSLRuntimeConfig(builderDistro); err != nil {
		return err
	}

	meta := *baseMeta
	meta.Image = opts.Tag
	meta.Distro = finalDistro
	meta.RootFSTar = finalRootFSTar
	meta.InstallPath = finalInstallDir

	for index, inst := range instructions[1:] {
		fmt.Printf("==> applying instruction %d/%d: %s %s\n", index+1, len(instructions)-1, inst.Op, inst.Args)

		if err := applyBuildInstruction(builderDistro, opts.ContextDir, &meta, inst); err != nil {
			return fmt.Errorf("Dockerfile line %d (%s): %w", inst.Line, inst.Raw, err)
		}
	}

	fmt.Println("==> exporting final rootfs")
	if err := os.MkdirAll(finalWorkDir, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(finalInstallDir, 0755); err != nil {
		return err
	}

	if err := exportDistroRootFS(builderDistro, finalRootFSTar); err != nil {
		return err
	}

	if err := writeJSON(finalMetadataPath, meta); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("✅ built:", opts.Tag)
	fmt.Println("name:    ", displayNameFromDistro(meta.Distro))
	fmt.Println("distro:  ", meta.Distro)
	fmt.Println("base:    ", baseImage)
	fmt.Println("metadata:", finalMetadataPath)
	fmt.Println("rootfs:  ", finalRootFSTar)

	if len(meta.Volumes) > 0 {
		fmt.Println()
		fmt.Println("⚠️  declared persistent volumes:")
		for _, volume := range meta.Volumes {
			fmt.Println("   ", volume)
		}
	}

	return nil
}

func runPush(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: wincontainer push <name> <target-ref>")
	}

	sourceName := args[0]
	targetRef := args[1]

	meta, err := loadMetadata(sourceName)
	if err != nil {
		return err
	}

	return pushImage(*meta, targetRef)
}

func pushImage(meta ImageMetadata, targetRef string) error {
	if strings.TrimSpace(meta.RootFSTar) == "" {
		return fmt.Errorf("metadata has no rootfsTar")
	}

	if _, err := os.Stat(meta.RootFSTar); err != nil {
		return fmt.Errorf("rootfs not found for %s: %w", displayNameFromDistro(meta.Distro), err)
	}

	if meta.OS == "" {
		meta.OS = "linux"
	}
	if meta.Arch == "" {
		meta.Arch = "amd64"
	}

	ref, err := name.ParseReference(targetRef, name.WeakValidation)
	if err != nil {
		return err
	}

	fmt.Println("==> loading local workload:", displayNameFromDistro(meta.Distro))
	fmt.Println("==> rootfs:", meta.RootFSTar)
	fmt.Println("==> target:", targetRef)
	fmt.Println("==> creating OCI layer from rootfs")

	layer, err := tarball.LayerFromFile(meta.RootFSTar)
	if err != nil {
		return err
	}

	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		return err
	}

	cfg, err := img.ConfigFile()
	if err != nil {
		return err
	}

	cfg.OS = meta.OS
	cfg.Architecture = meta.Arch
	cfg.Config.Env = append([]string{}, meta.Env...)
	cfg.Config.Entrypoint = append([]string{}, meta.Entrypoint...)
	cfg.Config.Cmd = append([]string{}, meta.Cmd...)
	cfg.Config.WorkingDir = meta.WorkingDir
	cfg.Config.User = meta.User
	cfg.Config.Volumes = stringSetMap(meta.Volumes)
	cfg.Config.ExposedPorts = stringSetMap(meta.ExposedPorts)

	img, err = mutate.ConfigFile(img, cfg)
	if err != nil {
		return err
	}

	digest, digestErr := img.Digest()

	fmt.Println("==> pushing:", targetRef)
	if err := remote.Write(
		ref,
		img,
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("✅ pushed:", targetRef)
	if digestErr == nil {
		fmt.Println("digest:  ", digest.String())
	}
	fmt.Println()
	fmt.Println("You can test it with:")
	fmt.Println("  docker run --rm " + targetRef)

	return nil
}

func stringSetMap(values []string) map[string]struct{} {
	result := map[string]struct{}{}

	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}

		result[value] = struct{}{}
	}

	if len(result) == 0 {
		return nil
	}

	return result
}

func parseDockerfile(dockerfilePath string) ([]DockerInstruction, error) {
	b, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(b), "\n")
	instructions := []DockerInstruction{}

	current := ""
	currentLine := 0

	for i, raw := range lines {
		lineNo := i + 1
		line := strings.TrimSpace(raw)

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if current == "" {
			currentLine = lineNo
		}

		if strings.HasSuffix(line, "\\") {
			current += strings.TrimSpace(strings.TrimSuffix(line, "\\")) + " "
			continue
		}

		current += line

		inst, err := parseDockerfileInstruction(current, currentLine)
		if err != nil {
			return nil, err
		}

		instructions = append(instructions, inst)
		current = ""
		currentLine = 0
	}

	if strings.TrimSpace(current) != "" {
		inst, err := parseDockerfileInstruction(current, currentLine)
		if err != nil {
			return nil, err
		}
		instructions = append(instructions, inst)
	}

	return instructions, nil
}

func parseDockerfileInstruction(line string, lineNo int) (DockerInstruction, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return DockerInstruction{}, fmt.Errorf("empty Dockerfile instruction")
	}

	op := strings.ToUpper(fields[0])
	args := strings.TrimSpace(line[len(fields[0]):])

	return DockerInstruction{
		Op:   op,
		Args: args,
		Line: lineNo,
		Raw:  line,
	}, nil
}

func parseFromImage(args string) (string, error) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return "", fmt.Errorf("FROM requires an image")
	}

	for len(fields) > 0 && strings.HasPrefix(fields[0], "--") {
		fields = fields[1:]
	}

	if len(fields) == 0 {
		return "", fmt.Errorf("FROM requires an image")
	}

	return fields[0], nil
}

func applyBuildInstruction(builderDistro string, contextDir string, meta *ImageMetadata, inst DockerInstruction) error {
	switch inst.Op {
	case "RUN":
		if strings.TrimSpace(inst.Args) == "" {
			return fmt.Errorf("RUN requires a command")
		}
		return runBuildShell(builderDistro, meta, inst.Args)

	case "WORKDIR":
		workdir := normalizeContainerPath(inst.Args, meta.WorkingDir)
		if workdir == "" {
			return fmt.Errorf("WORKDIR requires a path")
		}
		meta.WorkingDir = workdir
		return runBuilderCommand(builderDistro, "mkdir -p "+shellQuote(workdir))

	case "COPY", "ADD":
		copies, err := parseCopyLike(inst.Args)
		if err != nil {
			return err
		}
		return applyBuildCopy(builderDistro, contextDir, meta.WorkingDir, copies)

	case "ENV":
		envs, err := parseDockerfileEnv(inst.Args)
		if err != nil {
			return err
		}
		for _, kv := range envs {
			key, _, _, err := parseEnvironment(kv)
			if err != nil {
				return err
			}
			meta.Env = setEnvValue(meta.Env, key, kv)
		}
		return nil

	case "EXPOSE":
		ports := strings.Fields(inst.Args)
		if len(ports) == 0 {
			return fmt.Errorf("EXPOSE requires at least one port")
		}
		for _, port := range ports {
			meta.ExposedPorts = appendUnique(meta.ExposedPorts, normalizeExposedPort(port))
		}
		sort.Strings(meta.ExposedPorts)
		return nil

	case "VOLUME":
		volumes, err := parseStringListOrFields(inst.Args)
		if err != nil {
			return err
		}
		if len(volumes) == 0 {
			return fmt.Errorf("VOLUME requires at least one path")
		}
		for _, volume := range volumes {
			volume = normalizeContainerPath(volume, meta.WorkingDir)
			if volume == "" {
				continue
			}
			meta.Volumes = appendUnique(meta.Volumes, volume)
			if err := runBuilderCommand(builderDistro, "mkdir -p "+shellQuote(volume)); err != nil {
				return err
			}
		}
		sort.Strings(meta.Volumes)
		return nil

	case "USER":
		meta.User = strings.TrimSpace(inst.Args)
		return nil

	case "CMD":
		cmd, err := parseCommandInstruction(inst.Args)
		if err != nil {
			return err
		}
		meta.Cmd = cmd
		return nil

	case "ENTRYPOINT":
		entrypoint, err := parseCommandInstruction(inst.Args)
		if err != nil {
			return err
		}
		meta.Entrypoint = entrypoint
		return nil

	case "LABEL", "ARG", "SHELL", "STOPSIGNAL", "HEALTHCHECK", "ONBUILD":
		fmt.Println("    warning: instruction ignored in this build MVP:", inst.Op)
		return nil

	default:
		return fmt.Errorf("unsupported instruction %s", inst.Op)
	}
}

func runBuildShell(builderDistro string, meta *ImageMetadata, command string) error {
	lines := []string{}

	if meta.WorkingDir != "" {
		lines = append(lines, "cd "+shellQuote(meta.WorkingDir))
	}

	allEnv := append([]string{}, meta.Env...)
	envArgs := []string{}
	for _, kv := range allEnv {
		key, value, hasValue, err := parseEnvironment(kv)
		if err != nil || !hasValue {
			continue
		}
		envArgs = append(envArgs, key+"="+value)
	}

	runCommand := "sh -lc " + shellQuote(command)
	if len(envArgs) > 0 {
		runCommand = "env " + shellJoin(envArgs) + " " + runCommand
	}

	lines = append(lines, runCommand)
	return runBuilderCommand(builderDistro, strings.Join(lines, " && "))
}

func runBuilderCommand(builderDistro string, script string) error {
	cmd := exec.Command("wsl.exe", "-d", builderDistro, "--cd", "/", "--", "sh", "-lc", script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

type CopySpec struct {
	Sources []string
	Dest    string
}

func parseCopyLike(args string) (CopySpec, error) {
	args = strings.TrimSpace(args)
	if args == "" {
		return CopySpec{}, fmt.Errorf("COPY requires source and destination")
	}

	if strings.HasPrefix(args, "[") {
		values, err := parseJSONStringArray(args)
		if err != nil {
			return CopySpec{}, err
		}
		if len(values) < 2 {
			return CopySpec{}, fmt.Errorf("COPY JSON form requires at least source and destination")
		}
		return CopySpec{Sources: values[:len(values)-1], Dest: values[len(values)-1]}, nil
	}

	fields := strings.Fields(args)
	filtered := []string{}
	for _, field := range fields {
		if strings.HasPrefix(field, "--from=") || field == "--from" {
			return CopySpec{}, fmt.Errorf("COPY --from is not supported in build MVP")
		}
		if strings.HasPrefix(field, "--") {
			continue
		}
		filtered = append(filtered, field)
	}

	if len(filtered) < 2 {
		return CopySpec{}, fmt.Errorf("COPY requires source and destination")
	}

	return CopySpec{Sources: filtered[:len(filtered)-1], Dest: filtered[len(filtered)-1]}, nil
}

func applyBuildCopy(builderDistro string, contextDir string, workingDir string, spec CopySpec) error {
	if len(spec.Sources) == 0 {
		return fmt.Errorf("COPY requires source")
	}

	dest := normalizeContainerPath(spec.Dest, workingDir)
	if dest == "" {
		return fmt.Errorf("COPY destination is empty")
	}

	multipleSources := len(spec.Sources) > 1
	destIsDir := multipleSources || strings.HasSuffix(spec.Dest, "/") || strings.HasSuffix(spec.Dest, "\\") || remotePathIsDir(builderDistro, dest)

	tmpTar, err := os.CreateTemp("", "wincontainer-copy-*.tar")
	if err != nil {
		return err
	}
	tmpTarPath := tmpTar.Name()
	defer os.Remove(tmpTarPath)

	tw := tar.NewWriter(tmpTar)

	for _, src := range spec.Sources {
		src = strings.Trim(src, `"'`)
		if src == "" {
			continue
		}

		srcPath := filepath.Join(contextDir, filepath.FromSlash(src))
		srcPath, err = filepath.Abs(srcPath)
		if err != nil {
			_ = tw.Close()
			_ = tmpTar.Close()
			return err
		}

		if !isPathWithin(srcPath, contextDir) {
			_ = tw.Close()
			_ = tmpTar.Close()
			return fmt.Errorf("COPY source escapes build context: %s", src)
		}

		info, err := os.Stat(srcPath)
		if err != nil {
			_ = tw.Close()
			_ = tmpTar.Close()
			return err
		}

		target := dest
		srcClean := path.Clean(filepath.ToSlash(src))
		if destIsDir && srcClean != "." {
			target = path.Join(dest, info.Name())
		}

		if err := addPathToTar(tw, srcPath, target); err != nil {
			_ = tw.Close()
			_ = tmpTar.Close()
			return err
		}
	}

	if err := tw.Close(); err != nil {
		_ = tmpTar.Close()
		return err
	}
	if err := tmpTar.Close(); err != nil {
		return err
	}

	f, err := os.Open(tmpTarPath)
	if err != nil {
		return err
	}
	defer f.Close()

	cmd := exec.Command("wsl.exe", "-d", builderDistro, "--cd", "/", "--", "tar", "-C", "/", "-xf", "-")
	cmd.Stdin = f
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("extract COPY tar into builder: %w", err)
	}

	return nil
}

func remotePathIsDir(builderDistro string, remotePath string) bool {
	cmd := exec.Command("wsl.exe", "-d", builderDistro, "--cd", "/", "--", "sh", "-lc", "[ -d "+shellQuote(remotePath)+" ]")
	return cmd.Run() == nil
}

func addPathToTar(tw *tar.Writer, srcPath string, destPath string) error {
	info, err := os.Lstat(srcPath)
	if err != nil {
		return err
	}

	destPath = strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(destPath)), "/")
	if destPath == "." || destPath == "" {
		return fmt.Errorf("invalid COPY destination")
	}

	if info.IsDir() {
		return filepath.Walk(srcPath, func(current string, currentInfo os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}

			rel, err := filepath.Rel(srcPath, current)
			if err != nil {
				return err
			}

			target := destPath
			if rel != "." {
				target = path.Join(destPath, filepath.ToSlash(rel))
			}

			return writeTarEntry(tw, current, target, currentInfo)
		})
	}

	return writeTarEntry(tw, srcPath, destPath, info)
}

func writeTarEntry(tw *tar.Writer, srcPath string, destPath string, info os.FileInfo) error {
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}

	hdr.Name = strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(destPath)), "/")

	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(srcPath)
		if err != nil {
			return err
		}
		hdr.Linkname = link
	}

	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}

	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}

	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(tw, f)
	return err
}

func parseDockerfileEnv(args string) ([]string, error) {
	args = strings.TrimSpace(args)
	if args == "" {
		return nil, fmt.Errorf("ENV requires arguments")
	}

	if strings.Contains(args, "=") {
		fields := strings.Fields(args)
		result := []string{}

		for _, field := range fields {
			key, value, hasValue, err := parseEnvironment(field)
			if err != nil || !hasValue {
				return nil, fmt.Errorf("invalid ENV assignment %q", field)
			}
			result = append(result, key+"="+value)
		}

		return result, nil
	}

	parts := strings.Fields(args)
	if len(parts) < 2 {
		return nil, fmt.Errorf("ENV requires KEY VALUE or KEY=VALUE")
	}

	return []string{parts[0] + "=" + strings.Join(parts[1:], " ")}, nil
}

func setEnvValue(env []string, key string, kv string) []string {
	result := []string{}

	for _, existing := range env {
		existingKey, _, _, err := parseEnvironment(existing)
		if err == nil && existingKey == key {
			continue
		}
		result = append(result, existing)
	}

	return append(result, kv)
}

func appendUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}

	for _, existing := range values {
		if existing == value {
			return values
		}
	}

	return append(values, value)
}

func normalizeExposedPort(portValue string) string {
	portValue = strings.TrimSpace(portValue)
	if portValue == "" {
		return portValue
	}

	if strings.Contains(portValue, "/") {
		return portValue
	}

	return portValue + "/tcp"
}

func parseStringListOrFields(args string) ([]string, error) {
	args = strings.TrimSpace(args)
	if args == "" {
		return []string{}, nil
	}

	if strings.HasPrefix(args, "[") {
		return parseJSONStringArray(args)
	}

	return strings.Fields(args), nil
}

func parseCommandInstruction(args string) ([]string, error) {
	args = strings.TrimSpace(args)
	if args == "" {
		return []string{}, nil
	}

	if strings.HasPrefix(args, "[") {
		return parseJSONStringArray(args)
	}

	return []string{"/bin/sh", "-c", args}, nil
}

func parseJSONStringArray(value string) ([]string, error) {
	var result []string
	if err := json.Unmarshal([]byte(value), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func normalizeContainerPath(value string, workingDir string) string {
	value = strings.TrimSpace(strings.Trim(value, `"'`))
	if value == "" {
		return ""
	}

	value = strings.ReplaceAll(value, "\\", "/")

	if strings.HasPrefix(value, "/") {
		return path.Clean(value)
	}

	if workingDir == "" {
		workingDir = "/"
	}

	return path.Clean(path.Join(workingDir, value))
}

func isPathWithin(candidate string, root string) bool {
	candidate = filepath.Clean(candidate)
	root = filepath.Clean(root)

	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}

	return rel == "." || (!strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "..")
}

func exportDistroRootFS(distroName string, outTar string) error {
	cmd := exec.Command("wsl.exe", "--export", distroName, outTar)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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
		Image:        imageRef,
		Distro:       distroName,
		OS:           cfg.OS,
		Arch:         cfg.Architecture,
		Env:          cfg.Config.Env,
		Entrypoint:   cfg.Config.Entrypoint,
		Cmd:          cfg.Config.Cmd,
		WorkingDir:   cfg.Config.WorkingDir,
		User:         cfg.Config.User,
		Volumes:      extractVolumes(cfg),
		ExposedPorts: extractExposedPorts(cfg),
		RootFSTar:    rootfsTar,
		InstallPath:  installDir,
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
	fmt.Println("    expose:    ", strings.Join(meta.ExposedPorts, " "))

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

func extractExposedPorts(cfg *v1.ConfigFile) []string {
	ports := []string{}

	for port := range cfg.Config.ExposedPorts {
		port = strings.TrimSpace(port)
		if port == "" {
			continue
		}
		ports = append(ports, port)
	}

	sort.Strings(ports)
	return ports
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
	materializeHardLinks(entries)

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

func materializeHardLinks(entries map[string]*fsEntry) {
	for name, entry := range entries {
		if entry == nil || entry.Header == nil || entry.Header.Typeflag != tar.TypeLink {
			continue
		}

		target := resolveHardLinkEntry(entries, name, entry.Header.Linkname, map[string]bool{})
		if target == nil || target.Header == nil {
			continue
		}

		if target.Header.Typeflag != tar.TypeReg && target.Header.Typeflag != tar.TypeRegA {
			continue
		}

		h := *entry.Header
		h.Typeflag = tar.TypeReg
		h.Linkname = ""
		h.Size = target.Header.Size
		if h.Mode == 0 {
			h.Mode = target.Header.Mode
		}

		entries[name] = &fsEntry{
			Header:  &h,
			Blob:    target.Blob,
			Content: target.Content,
		}
	}
}

func resolveHardLinkEntry(entries map[string]*fsEntry, currentName string, linkName string, seen map[string]bool) *fsEntry {
	for _, candidate := range hardLinkTargetCandidates(currentName, linkName) {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true

		entry := entries[candidate]
		if entry == nil || entry.Header == nil {
			continue
		}

		if entry.Header.Typeflag == tar.TypeLink {
			resolved := resolveHardLinkEntry(entries, candidate, entry.Header.Linkname, seen)
			if resolved != nil {
				return resolved
			}
			continue
		}

		return entry
	}

	return nil
}

func hardLinkTargetCandidates(currentName string, linkName string) []string {
	seen := map[string]bool{}
	candidates := []string{}

	add := func(value string) {
		clean, ok := cleanTarName(value)
		if !ok || seen[clean] {
			return
		}
		seen[clean] = true
		candidates = append(candidates, clean)
	}

	add(linkName)

	if linkName != "" && !strings.HasPrefix(linkName, "/") {
		add(path.Join(path.Dir(currentName), linkName))
	}

	return candidates
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
