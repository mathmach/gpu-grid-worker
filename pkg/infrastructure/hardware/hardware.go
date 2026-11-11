package hardware

import (
	"context"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/llama"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/workerconfig"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

const defaultGPUProbeTimeout = 15 * time.Second

var (
	rxVulkanDevice = regexp.MustCompile(`(?m)^\s*(Vulkan\d+):\s*(.+?)\s*\((\d+)\s*MiB,\s*(\d+)\s*MiB free\)`)
	rxDedicatedGPU = regexp.MustCompile(`(?i)NVIDIA|GeForce|RTX|GTX|Quadro|Tesla|H100|H200|A100|A800|L40|B100|B200|Radeon|Instinct|AMD|Arc|Data Center GPU|DISCRETE_GPU`)

	softwareAdapterMarkers = []string{
		"llvmpipe",
		"lavapipe",
		"swiftshader",
		"microsoft basic render",
		"software adapter",
		"(cpu)",
		"cpu-only",
	}

	integratedGPUMarkers = []string{
		"integrated_gpu",
		"(integrated",
		"uhd graphics",
		"hd graphics",
		"iris(r) xe",
		"iris xe",
		"iris plus",
		"iris pro",
		"intel(r) graphics",
		"radeon(tm) graphics",
		"radeon graphics",
		"vega mobile",
		"radeon 780m",
		"radeon 760m",
		"radeon 740m",
		"radeon 680m",
		"radeon 660m",
		"radeon 890m",
		"radeon 880m",
	}

	virtualNICPrefixes = []string{
		"lo",
		"docker",
		"veth",
		"br-",
		"virbr",
		"cni",
		"flannel",
		"calico",
		"weave",
		"kube",
		"tun",
		"tap",
		"utun",
		"wg",
		"tailscale",
		"zerotier",
		"vboxnet",
		"vmnet",
		"wsl",
		"vethernet",
	}
)

func ErrNoVulkanGPU() error {
	return derr.New(derr.CodeNoVulkanGPU, 422, "no dedicated or Vulkan-compatible GPU detected on this machine", nil)
}

func IsDedicatedGPU(name string) bool {
	clean := strings.TrimSpace(name)
	if clean == "" || isSoftwareOrCPUAdapter(clean) || isIntegratedGPUName(clean) {
		return false
	}
	return rxDedicatedGPU.MatchString(clean)
}

func isSoftwareOrCPUAdapter(name string) bool {
	lower := strings.ToLower(name)
	for _, marker := range softwareAdapterMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isIntegratedGPUName(name string) bool {
	lower := strings.ToLower(name)
	for _, marker := range integratedGPUMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func MachineFingerprint() string {
	raw := strings.TrimSpace(readOSMachineID())
	if raw == "" {
		raw = strings.TrimSpace(fallbackHardwareFingerprint())
	}
	if raw == "" {
		return ""
	}
	return nodecore.ComputeMachineID("gpu-grid-v1", strings.ToLower(raw)).String()
}

func readOSMachineID() string {
	switch runtime.GOOS {
	case "windows":
		return readWindowsMachineGUID()
	case "linux":
		for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
			if b, err := os.ReadFile(p); err == nil {
				if trimmed := strings.TrimSpace(string(b)); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

func fallbackHardwareFingerprint() string {
	macs := collectStableHardwareMACs()
	var cleanHost string
	if host, err := os.Hostname(); err == nil {
		cleanHost = strings.TrimSpace(host)
	}
	if cleanHost == "" && len(macs) == 0 {
		return ""
	}
	return cleanHost + "|" + strings.Join(macs, ",")
}

func collectStableHardwareMACs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var physical []string
	var fallback []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || len(iface.HardwareAddr) == 0 {
			continue
		}
		mac := strings.ToLower(strings.TrimSpace(iface.HardwareAddr.String()))
		if mac == "" {
			continue
		}
		fallback = append(fallback, mac)
		if iface.Flags&net.FlagPointToPoint == 0 && !isVirtualOrOverlayNIC(iface.Name) {
			physical = append(physical, mac)
		}
	}
	chosen := physical
	if len(chosen) == 0 {
		chosen = fallback
	}
	slices.Sort(chosen)
	return slices.Compact(chosen)
}

func isVirtualOrOverlayNIC(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, prefix := range virtualNICPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func ProbeVulkanGPUs(ctx context.Context, runtimeDir string) ([]protocol.GpuInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultGPUProbeTimeout)
		defer cancel()
	}

	probeBin := resolveProbeBinary(runtimeDir)
	if probeBin == "" {
		return nil, ErrNoVulkanGPU()
	}

	cmd := exec.CommandContext(ctx, probeBin, "--list-devices")
	cmd.Dir = runtimeDir
	cmd.Env = workerconfig.RuntimePathEnv(runtimeDir)
	llama.PrepareSubprocess(cmd)

	out, err := cmd.CombinedOutput()
	devices := ParseVulkanDeviceList(string(out))
	if len(devices) == 0 {
		if err != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrNoVulkanGPU()
	}
	return devices, nil
}

func ParseVulkanDeviceList(raw string) []protocol.GpuInfo {
	matches := rxVulkanDevice.FindAllStringSubmatch(raw, -1)
	var devices []protocol.GpuInfo
	for _, m := range matches {
		cleanName := strings.TrimSpace(m[2])
		if cleanName == "" || isSoftwareOrCPUAdapter(cleanName) {
			continue
		}
		tot, errTot := strconv.Atoi(m[3])
		free, errFree := strconv.Atoi(m[4])
		if errTot != nil || errFree != nil || (tot <= 0 && free <= 0) {
			continue
		}
		devices = append(devices, protocol.GpuInfo{
			DeviceID:     m[1],
			Name:         cleanName,
			VramTotalMiB: tot,
			VramFreeMiB:  free,
		})
	}
	slices.SortStableFunc(devices, compareVulkanDevicePriority)
	return devices
}

func compareVulkanDevicePriority(a, b protocol.GpuInfo) int {
	aDed := IsDedicatedGPU(a.Name)
	bDed := IsDedicatedGPU(b.Name)
	if aDed == bDed {
		return 0
	}
	if aDed {
		return -1
	}
	return 1
}

func resolveProbeBinary(runtimeDir string) string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	for _, name := range []string{"llama-server" + ext, "llama-cli" + ext, "ggml-rpc-server" + ext} {
		if found := llama.FindExecutableRecursive(runtimeDir, name); found != "" {
			return found
		}
	}
	return ""
}

type VulkanInspector struct{}

func NewVulkanInspector() *VulkanInspector {
	return &VulkanInspector{}
}

func (v *VulkanInspector) MachineFingerprint() string {
	return MachineFingerprint()
}

func (v *VulkanInspector) ProbeGPUs(ctx context.Context, runtimeDir string) ([]protocol.GpuInfo, error) {
	return ProbeVulkanGPUs(ctx, runtimeDir)
}

func (v *VulkanInspector) IsDedicatedGPU(name string) bool {
	return IsDedicatedGPU(name)
}
