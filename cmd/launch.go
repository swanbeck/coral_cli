package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"

	"coral_cli/internal/cleanup"
	"coral_cli/internal/compose"
	"coral_cli/internal/health"
	"coral_cli/internal/libs"
	"coral_cli/internal/logging"
	"coral_cli/internal/registry"
	"coral_cli/internal/util"
)

var (
	launchComposePath   string
	launchEnvFile       string
	launchHandle        string
	launchGroup         string
	launchDetached      bool
	launchKill          bool
	launchExecutorDelay float32
	launchProfiles      []string
	launchLibDir        string
	launchHealthTimeout float32
)

func init() {
	launchCmd.Args = cobra.NoArgs

	launchCmd.Flags().StringVarP(&launchComposePath, "compose-file", "f", "", "Path to Docker Compose .yaml file to start services")
	launchCmd.Flags().StringVar(&launchEnvFile, "env-file", "", "Optional path to .env file to use for compose file substitutions")
	launchCmd.Flags().StringVar(&launchHandle, "handle", "", "Optional handle for this instance")
	launchCmd.Flags().StringVarP(&launchGroup, "group", "g", "coral", "Optional group for this instance")
	launchCmd.Flags().BoolVarP(&launchDetached, "detached", "d", false, "Launch in detached mode")
	launchCmd.Flags().BoolVar(&launchKill, "kill", true, "Forcefully kills instances before removing them")
	launchCmd.Flags().Float32Var(&launchExecutorDelay, "executor-delay", 0.0, "Additional delay in seconds after health checks pass before starting executors")
	launchCmd.Flags().StringSliceVarP(&launchProfiles, "profile", "p", []string{}, "List of profiles to launch (drivers, skillsets, executors); if not specified, all profiles will be launched")
	launchCmd.Flags().StringVar(&launchLibDir, "lib-dir", "", "Override CORAL_LIB path (takes precedence over $CORAL_LIB environment variable)")
	launchCmd.Flags().Float32Var(&launchHealthTimeout, "health-timeout", 120.0, "Seconds to wait for drivers/skillsets to become healthy before starting executors")

	launchCmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if toComplete == "" {
			var out []string
			cmd.Flags().VisitAll(func(f *pflag.Flag) {
				if f.Shorthand != "" {
					out = append(out, fmt.Sprintf("#   --%s,-%s", f.Name, f.Shorthand))
				} else {
					out = append(out, fmt.Sprintf("#   --%s", f.Name))
				}
			})
			return out, cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	launchCmd.RegisterFlagCompletionFunc("compose-file", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		dir := "."
		if strings.Contains(toComplete, string(os.PathSeparator)) {
			dir = filepath.Dir(toComplete)
			if dir == "" {
				dir = "."
			}
		}
		files, err := os.ReadDir(dir)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		var suggestions []string
		for _, f := range files {
			entry := filepath.Join(dir, f.Name())
			display := entry
			if f.IsDir() {
				display += string(os.PathSeparator)
			}
			if !strings.HasPrefix(display, toComplete) {
				continue
			}
			if f.IsDir() {
				suggestions = append(suggestions, display)
				continue
			}
			if !strings.HasSuffix(f.Name(), ".yaml") && !strings.HasSuffix(f.Name(), ".yml") {
				continue
			}
			content, err := os.ReadFile(entry)
			if err != nil {
				continue
			}
			var doc map[string]any
			if err := yaml.Unmarshal(content, &doc); err != nil {
				continue
			}
			if _, ok := doc["services"]; ok {
				suggestions = append(suggestions, display)
			}
		}
		return suggestions, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	})

	launchCmd.RegisterFlagCompletionFunc("profile", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		profiles := []string{"drivers", "skillsets", "executors"}
		var matches []string
		for _, profile := range profiles {
			if strings.HasPrefix(profile, toComplete) {
				matches = append(matches, profile)
			}
		}
		return matches, cobra.ShellCompDirectiveNoFileComp
	})
}

var launchCmd = &cobra.Command{
	Use:   "launch",
	Short: "Launches Coral instances",
	RunE: func(cmd *cobra.Command, args []string) error {
		return launch(launchComposePath, launchEnvFile, launchHandle, launchGroup,
			launchDetached, launchKill, launchExecutorDelay, launchHealthTimeout,
			launchLibDir, launchProfiles)
	},
}

func launch(composePath, envFile, handle, group string, detached, kill bool,
	executorDelay, healthTimeout float32, libDirOverride string, profilesToStart []string) error {

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	var receivedSignal atomic.Bool
	go func() {
		<-signalChan
		receivedSignal.Store(true)
	}()

	// load environment
	env := make(map[string]string)
	resolvedEnvFile, err := util.ResolveEnvFile(envFile)
	if err != nil {
		return fmt.Errorf("resolving env file: %w", err)
	}
	if resolvedEnvFile != "" {
		env, err = compose.LoadEnvFile(resolvedEnvFile)
		if err != nil {
			return fmt.Errorf("loading .env: %w", err)
		}
	}
	for _, e := range os.Environ() {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			if _, exists := env[parts[0]]; !exists {
				env[parts[0]] = parts[1]
			}
		}
	}

	// resolve compose file
	resolvedComposePath, err := util.ResolveComposeFile(composePath)
	if err != nil {
		return err
	}
	parsedCompose, err := compose.ParseCompose(resolvedComposePath, env)
	if err != nil {
		return fmt.Errorf("parsing compose file: %w", err)
	}

	// resolve lib path
	var libPath string
	if libDirOverride != "" {
		libPath = libDirOverride
	} else {
		libPath = env["CORAL_LIB"]
	}
	if strings.TrimSpace(libPath) == "" {
		libPath, err = filepath.Abs("./lib")
		if err != nil {
			return fmt.Errorf("resolving ./lib: %w", err)
		}
		if _, err := os.Stat(libPath); os.IsNotExist(err) {
			if err := os.Mkdir(libPath, 0755); err != nil {
				return fmt.Errorf("creating lib dir: %w", err)
			}
		}
	} else {
		if _, err := os.Stat(libPath); os.IsNotExist(err) {
			return fmt.Errorf("lib dir %q does not exist", libPath)
		}
	}

	// when CORAL runs inside Docker, Docker volume mounts in compose files need host paths; note that docker cp operations stream through the socket so no longer require special handling
	isDocker := env["CORAL_IS_DOCKER"]
	var hostLibPath string
	if isDocker == "true" {
		var ok bool
		hostLibPath, ok = env["CORAL_HOST_LIB"]
		if !ok || strings.TrimSpace(hostLibPath) == "" {
			return fmt.Errorf("CORAL_HOST_LIB is required when CORAL_IS_DOCKER=true")
		}
	}

	if err := checkImagesLocal(parsedCompose); err != nil {
		return fmt.Errorf("checking images: %w", err)
	}

	uid := uuid.New()
	instanceName := fmt.Sprintf("coral-%x", uid[:4])
	fmt.Println(logging.Info("Launching new instance " + logging.BoldMagentaHi(instanceName)))

	// load (or create) the persistent registry
	reg, err := registry.Load(libPath)
	if err != nil {
		return fmt.Errorf("loading registry: %w", err)
	}

	// declared here so the deferred abort below can reference it even if launch fails before writeComposeToDisk is reached
	outputPath := filepath.Join(libPath, "compose", instanceName+".yaml")

	// guard: if launch fails before the instance is fully handed off to the run functions (which have their own cleanup), abort removes any staging dirs and registry records written so far
	launched := false
	defer func() {
		if !launched {
			cleanup.AbortInstance(instanceName, libPath, outputPath, reg)
		}
	}()

	mergedCompose, profilesMap, err := buildMergedCompose(
		parsedCompose, libPath, hostLibPath, profilesToStart, instanceName, reg)
	if err != nil {
		return err
	}

	profiles := extractProfileNames(profilesMap)
	servicesRaw, ok := mergedCompose["services"]
	if !ok {
		return fmt.Errorf("merged compose file has no 'services' section")
	}
	services, ok := servicesRaw.(map[string]interface{})
	if !ok || len(services) == 0 {
		return fmt.Errorf("merged compose file has no valid services")
	}

	if err := writeComposeToDisk(outputPath, mergedCompose); err != nil {
		return err
	}
	if err := writeInstanceMetadata(instanceName, outputPath, libPath, handle, group, detached); err != nil {
		return err
	}

	profiles = orderedProfiles(profiles)
	if len(profiles) == 0 {
		return fmt.Errorf("no valid profiles to run")
	}

	// past this point metadata is written; suppress the deferred abort and use RemoveInstanceFiles (which reads metadata) for any remaining cleanup
	launched = true

	if receivedSignal.Load() {
		fmt.Printf("\n%s\n", logging.Warning(fmt.Sprintf("Interrupt during init — cleaning up %s...", logging.BoldMagenta(instanceName))))
		cleanup.RemoveInstanceFiles(instanceName)
		fmt.Println(logging.Success("Done"))
		return nil
	}

	signal.Reset(os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	if detached {
		// give the health gate a context that the user can cancel with ctrl+c
		dCtx, dCancel := context.WithCancel(context.Background())
		defer dCancel()
		dSigCh := make(chan os.Signal, 1)
		signal.Notify(dSigCh, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			select {
			case <-dSigCh:
				dCancel()
				signal.Stop(dSigCh)
			case <-dCtx.Done():
			}
		}()
		if err := runDetached(dCtx, profiles, instanceName, outputPath, executorDelay, healthTimeout, profilesMap, reg); err != nil {
			if dCtx.Err() == nil {
				fmt.Println(logging.Warning(fmt.Sprintf("Launch failed — cleaning up %s...", logging.BoldMagenta(instanceName))))
				_ = cleanup.StopCompose(instanceName, outputPath, true, profiles)
				_ = cleanup.RemoveInstanceFiles(instanceName)
			}
			return err
		}
		return nil
	}
	return runForeground(profiles, instanceName, outputPath, kill, executorDelay, healthTimeout, profilesMap, reg)
}

var validProfiles = map[string]bool{"drivers": true, "skillsets": true, "executors": true}

func checkImagesLocal(cf *compose.ComposeFile) error {
	for name, svc := range cf.Services {
		raw, ok := svc["image"]
		if !ok || raw == nil {
			return fmt.Errorf("missing 'image' in service %s", name)
		}
		image, ok := raw.(string)
		if !ok {
			return fmt.Errorf("expected string for 'image' in service %s", name)
		}
		if _, err := libs.GetImageID(image); err != nil {
			return fmt.Errorf("checking image %s for service %s: %w", image, name, err)
		}
		labels, err := libs.GetImageLabels(image)
		if err != nil {
			return fmt.Errorf("reading labels for service %s: %w", name, err)
		}
		profile := labels["coral.profile"]
		if profile == "" {
			return fmt.Errorf("image %s (service %s) is missing required label coral.profile", image, name)
		}
		if !validProfiles[profile] {
			return fmt.Errorf("image %s (service %s) has invalid coral.profile %q: must be one of drivers, skillsets, executors", image, name, profile)
		}
	}
	return nil
}

// extracts library artifacts from each service image, records them in the registry, and builds the merged compose map
func buildMergedCompose(cf *compose.ComposeFile, lib, hostLib string,
	profilesToStart []string, instanceName string, reg *registry.Registry,
) (compose.RawCompose, map[string][]string, error) {

	rawCompose, err := cf.ToMap()
	if err != nil {
		return nil, nil, fmt.Errorf("converting compose to map: %w", err)
	}
	rawServices := rawCompose["services"].(map[string]interface{})
	merged := compose.RawCompose{"services": map[string]interface{}{}}
	profilesMap := map[string][]string{}

	// hostLib is used only for volume-mount path rewriting in compose files
	imageLib := lib
	if hostLib != "" {
		imageLib = hostLib
	}
	_ = imageLib // used below for volume path rewriting

	for name, svc := range cf.Services {
		image := svc["image"].(string)
		labels, err := libs.GetImageLabels(image)
		if err != nil {
			return nil, nil, fmt.Errorf("reading labels for service %s: %w", name, err)
		}
		profile := labels["coral.profile"] // already validated non-empty and valid in checkImagesLocal

		if len(profilesToStart) > 0 {
			matched := false
			for _, p := range profilesToStart {
				if p == profile {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		profilesMap[profile] = append(profilesMap[profile], name)

		stagingDir, imageID, err := libs.ExtractLibraries(image, name, lib)
		if err != nil {
			return nil, nil, fmt.Errorf("extracting %s for service %s: %w", image, name, err)
		}

		if err := reg.RecordExtraction(imageID, stagingDir, imageID, instanceName, labels["coral.btcpp_version"], labels["coral.ros_distro"]); err != nil {
			fmt.Println(logging.Warning(fmt.Sprintf("recording extraction for %s: %v", name, err)))
		}

		fmt.Println(logging.Info(fmt.Sprintf(
			"Extracted interfaces from %s for %s", image, logging.BoldMagenta(name))))

		// merge docker.yaml from the staging directory into the service config
		baseSvc := rawServices[name].(map[string]interface{})
		extractedPath := filepath.Join(stagingDir, "docker.yaml")
		var mergedSvc map[string]interface{}
		if _, err := os.Stat(extractedPath); err == nil {
			extracted, err := compose.LoadRawYAML(extractedPath)
			if err != nil {
				return nil, nil, fmt.Errorf("loading extracted compose for %s: %w", name, err)
			}
			mergedSvc = compose.MergeServiceConfigs(baseSvc, extracted)
		} else {
			mergedSvc = baseSvc
		}

		// resolve and inject device mappings from devices.yaml
		devicesPath := filepath.Join(stagingDir, "devices.yaml")
		if _, err := os.Stat(devicesPath); err == nil {
			df, err := compose.LoadDevicesFile(devicesPath)
			if err != nil {
				return nil, nil, fmt.Errorf("loading devices.yaml for %s: %w", name, err)
			}
			devPaths, err := compose.ResolveDevicePaths(df, name)
			if err != nil {
				return nil, nil, err
			}
			if len(devPaths) > 0 {
				existing, _ := mergedSvc["devices"].([]interface{})
				for _, p := range devPaths {
					existing = append(existing, p)
				}
				mergedSvc["devices"] = existing
				fmt.Println(logging.Info(fmt.Sprintf(
					"Mapped %d device(s) for %s", len(devPaths), logging.BoldMagenta(name))))
			}
		}

		// rewrite relative host volume paths to absolute - and when CORAL is running inside Docker, rebase them onto hostLib so the Docker daemon can reach them
		if volumes, ok := mergedSvc["volumes"].([]interface{}); ok {
			for i, v := range volumes {
				volStr, ok := v.(string)
				if !ok {
					continue
				}
				parts := strings.SplitN(volStr, ":", 2)
				if len(parts) != 2 {
					continue
				}
				hostPath := parts[0]
				if filepath.IsAbs(hostPath) || strings.HasPrefix(hostPath, "${") {
					// if the path is absolute and starts with libPath, rebase onto hostLib
					if hostLib != "" && strings.HasPrefix(hostPath, lib) {
						hostPath = hostLib + hostPath[len(lib):]
						volumes[i] = fmt.Sprintf("%s:%s", hostPath, parts[1])
					}
					continue
				}
				absPath, err := filepath.Abs(hostPath)
				if err == nil {
					volumes[i] = fmt.Sprintf("%s:%s", absPath, parts[1])
				}
			}
		}

		mergedSvc["profiles"] = []interface{}{profile}
		merged["services"].(map[string]interface{})[name] = mergedSvc
	}

	return merged, profilesMap, nil
}

// performs the three-phase executor launch:
//  1. docker compose create  — allocate containers without starting them
//  2. InjectLibraries        — copy behavior/interface .so files into each container, sourced from all staging dirs
//  3. docker compose start   — start the containers
func createAndStartExecutors(instanceName, composePath string, executorServices []string,
	reg *registry.Registry) error {

	createArgs := []string{"compose", "-p", instanceName, "-f", composePath, "--profile", "executors", "create"}
	createCmd := exec.Command("docker", createArgs...)
	createCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	createCmd.Stdout = os.Stdout
	createCmd.Stderr = os.Stderr
	if err := createCmd.Run(); err != nil {
		return fmt.Errorf("creating executor containers: %w", err)
	}

	allExtractions := reg.AllExtractions()

	for _, svc := range executorServices {
		containerID, err := health.GetContainerIDForService(instanceName, svc)
		if err != nil || containerID == "" {
			return fmt.Errorf("locating container for executor service %s: %w", svc, err)
		}

		execLabels, err := libs.GetContainerLabels(containerID)
		if err != nil {
			return fmt.Errorf("reading labels for executor %s: %w", svc, err)
		}
		execBtcpp := execLabels["coral.btcpp_version"]
		execRos := execLabels["coral.ros_distro"]

		compatibleDirs := make(map[string]string)
		for imageID, rec := range allExtractions {
			var reasons []string
			if rec.BtcppVersion != execBtcpp {
				reasons = append(reasons, fmt.Sprintf("BT.CPP version mismatch %q != %q", rec.BtcppVersion, execBtcpp))
			}
			if rec.RosDistro != execRos {
				reasons = append(reasons, fmt.Sprintf("ROS distro mismatch %q != %q", rec.RosDistro, execRos))
			}
			if len(reasons) > 0 {
				fmt.Println(logging.Warning(fmt.Sprintf(
					"Cowardly refusing to inject libraries from %s into executor %s: %s",
					rec.PayloadID, svc, strings.Join(reasons, ", "),
				)))
				continue
			}
			compatibleDirs[imageID] = rec.StagingDir
		}

		injected, err := libs.InjectLibraries(containerID, compatibleDirs)
		if err != nil {
			return fmt.Errorf("injecting libraries into %s: %w", svc, err)
		}
		if err := reg.RecordInjection(containerID, instanceName, injected); err != nil {
			fmt.Println(logging.Warning(fmt.Sprintf("recording injection for %s: %v", svc, err)))
		}
		active := 0
		for _, l := range injected {
			if !l.Shadowed {
				active++
			}
		}
		fmt.Println(logging.Info(fmt.Sprintf(
			"Injected %d libraries into executor %s", active, logging.BoldMagenta(svc))))
	}

	startArgs := append([]string{"compose", "-p", instanceName, "-f", composePath, "start"}, executorServices...)
	startCmd := exec.Command("docker", startArgs...)
	startCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	startCmd.Stdout = os.Stdout
	startCmd.Stderr = os.Stderr
	return startCmd.Run()
}

func runDetached(ctx context.Context, profiles []string, instanceName, composePath string,
	executorDelay, healthTimeout float32, profilesMap map[string][]string,
	reg *registry.Registry) error {

	for _, profile := range profiles {
		if profile == "executors" {
			// gate on drivers + skillsets being healthy before touching executors (only if they implement health checks)
			var depServices []string
			for _, p := range []string{"drivers", "skillsets"} {
				depServices = append(depServices, profilesMap[p]...)
			}
			if len(depServices) > 0 {
				fmt.Println(logging.Info("Waiting for drivers and skillsets to become healthy..."))
				timeout := time.Duration(healthTimeout * float32(time.Second))
				if err := health.WaitForHealthy(ctx, instanceName, depServices, timeout); err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					fmt.Println(logging.Warning(fmt.Sprintf("Health gate timed out: %v — proceeding anyway", err)))
				}
			}
			if executorDelay > 0 {
				fmt.Println(logging.Info(fmt.Sprintf("Waiting %.0fs before starting executors...", executorDelay)))
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(executorDelay) * time.Second):
				}
			}
			if err := createAndStartExecutors(instanceName, composePath, profilesMap["executors"], reg); err != nil {
				return fmt.Errorf("starting executors: %w", err)
			}
			continue
		}

		fmt.Println(logging.Info(fmt.Sprintf("Starting %s (%d): %s",
			logging.BoldMagenta(profile), len(profilesMap[profile]),
			logging.BoldMagenta(fmt.Sprintf("%v", profilesMap[profile])))))

		cmd := exec.Command("docker", "compose", "-p", instanceName, "-f", composePath,
			"--profile", profile, "up", "-d")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("starting profile %s: %w", profile, err)
		}
	}
	return nil
}

func runForeground(profiles []string, instanceName, composePath string, kill bool,
	executorDelay, healthTimeout float32, profilesMap map[string][]string,
	reg *registry.Registry) error {

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())

	var cleanupOnce sync.Once
	doCleanup := func() {
		cleanupOnce.Do(func() {
			signal.Ignore(syscall.SIGINT, syscall.SIGTERM)
			if err := cleanup.StopCompose(instanceName, composePath, kill, profiles); err != nil {
				fmt.Println(logging.Warning(fmt.Sprintf("stopping compose: %v", err)))
			}
			if err := cleanup.RemoveInstanceFiles(instanceName); err != nil {
				fmt.Println(logging.Failure(fmt.Sprintf("cleaning up files: %v", err)))
			}
			fmt.Println(logging.Success("Done"))
		})
	}
	// cancel before doCleanup so the health monitor stops first (LIFO defer order)
	defer doCleanup()
	defer cancel()

	shutdownChan := make(chan struct{})
	// start the signal goroutine before runDetached so a ctrl+c during the health gate cancels the context and unblocks WaitForHealthy immediately
	go func() {
		<-signalChan
		signal.Stop(signalChan)
		cancel()
		close(shutdownChan)
	}()

	if err := runDetached(ctx, profiles, instanceName, composePath, executorDelay, healthTimeout, profilesMap, reg); err != nil {
		if ctx.Err() != nil {
			fmt.Printf("\n%s\n", logging.Warning(fmt.Sprintf("Interrupt received — shutting down %s...", logging.BoldMagenta(instanceName))))
			return nil
		}
		return fmt.Errorf("starting profiles: %w", err)
	}

	// start health monitor after all profiles are running
	monitor := health.NewMonitor(instanceName, reg)
	healthEvents := monitor.Start(ctx)
	go func() {
		for range healthEvents {
			// events are already logged by the monitor; kernel integration in Phase 5
		}
	}()

	containers, err := logging.GetContainerInfo(instanceName, composePath)
	if err != nil {
		return fmt.Errorf("getting container info: %w", err)
	}

	doneChan, errCh := logging.TailLogs(containers, shutdownChan, true)

	select {
	case <-shutdownChan:
		fmt.Printf("\n%s\n", logging.Warning(fmt.Sprintf("Interrupt received — shutting down %s...", logging.BoldMagenta(instanceName))))
	case <-doneChan:
		fmt.Println(logging.Info("All log tails completed — shutting down..."))
	case err := <-errCh:
		fmt.Println(logging.Failure(fmt.Sprintf("Log streaming error: %v", err)))
	}

	return nil
}

func extractProfileNames(profiles map[string][]string) []string {
	var names []string
	for k := range profiles {
		names = append(names, k)
	}
	return names
}

func writeComposeToDisk(path string, data compose.RawCompose) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	return compose.SaveRawYAML(path, data)
}

func writeInstanceMetadata(instanceName, path, lib, handle, group string, detached bool) error {
	meta := util.InstanceMetadata{
		Name:        instanceName,
		ComposeFile: path,
		CreatedAt:   time.Now().Format(time.RFC3339),
		LibPath:     lib,
		Handle:      handle,
		Group:       group,
		Detached:    detached,
	}
	home, _ := os.UserHomeDir()
	storeDir := filepath.Join(home, ".coral_cli", "instances")
	os.MkdirAll(storeDir, 0755)
	data, _ := json.MarshalIndent(meta, "", "  ")
	return os.WriteFile(filepath.Join(storeDir, instanceName+".json"), data, 0644)
}

func orderedProfiles(input []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, key := range []string{"drivers", "skillsets", "executors"} {
		for _, profile := range input {
			if profile == key && !seen[profile] {
				result = append(result, profile)
				seen[profile] = true
			}
		}
	}
	return result
}
