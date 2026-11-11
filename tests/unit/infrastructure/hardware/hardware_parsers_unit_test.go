package hardware_test

import (
	"strings"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/hardware"
)

func hasCode(err error, code derr.Code) bool {
	appErr, ok := derr.AsAppError(err)
	return ok && appErr.Code == code
}

func TestIsDedicatedGPURejectsSoftwareAndIntegratedAdapters(t *testing.T) {
	inspector := hardware.NewVulkanInspector()

	for _, dedicated := range []string{
		"NVIDIA GeForce RTX 4090",
		"AMD Radeon RX 7900 XTX",
		"Intel(R) Arc(TM) A770 Graphics",
		"NVIDIA H100 PCIe",
		"Custom Vulkan Compute Card (DISCRETE_GPU)",
	} {
		if !hardware.IsDedicatedGPU(dedicated) || !inspector.IsDedicatedGPU(dedicated) {
			t.Fatalf("expected %q classified as dedicated GPU", dedicated)
		}
	}

	for _, nonDedicated := range []string{
		"",
		"   ",
		"llvmpipe (LLVM 17.0.6, 256 bits)",
		"lavapipe (Vulkan 1.3)",
		"Google SwiftShader",
		"Microsoft Basic Render Driver",
		"Software Adapter (CPU)",
		"Intel(R) UHD Graphics 770",
		"Intel(R) Iris(R) Xe Graphics",
		"AMD Radeon(TM) Graphics",
		"AMD Radeon 780M",
		"APU Graphics (INTEGRATED_GPU)",
	} {
		if hardware.IsDedicatedGPU(nonDedicated) || inspector.IsDedicatedGPU(nonDedicated) {
			t.Fatalf("expected %q rejected as non-dedicated adapter", nonDedicated)
		}
	}
}

func TestParseVulkanDeviceListFiltersSoftwareSortsDedicatedFirstAndPreservesVRAM(t *testing.T) {
	rawOutput := strings.Join([]string{
		"ggml_vulkan: Found 5 Vulkan devices:",
		"  Vulkan0: llvmpipe (LLVM 18.1.0, 256 bits) (32768 MiB, 31000 MiB free)",
		"  Vulkan1: AMD Radeon 780M (4096 MiB, 3584 MiB free)",
		"  Vulkan2: NVIDIA GeForce RTX 4090 (24564 MiB, 23910 MiB free)",
		"  Vulkan3: Broken Adapter (0 MiB, 0 MiB free)",
		"  Vulkan4: AMD Radeon RX 7900 XTX (24560 MiB, 24100 MiB free)",
	}, "\n")

	devices := hardware.ParseVulkanDeviceList(rawOutput)
	if len(devices) != 3 {
		t.Fatalf("expected 3 usable Vulkan GPUs (filtering llvmpipe and 0-MiB device), got %+v", devices)
	}

	if devices[0].DeviceID != "Vulkan2" || devices[0].Name != "NVIDIA GeForce RTX 4090" || devices[0].VramTotalMiB != 24564 || devices[0].VramFreeMiB != 23910 {
		t.Fatalf("expected first sorted device to be RTX 4090, got %+v", devices[0])
	}
	if devices[1].DeviceID != "Vulkan4" || devices[1].Name != "AMD Radeon RX 7900 XTX" || devices[1].VramTotalMiB != 24560 {
		t.Fatalf("expected second sorted device to be RX 7900 XTX, got %+v", devices[1])
	}
	if devices[2].DeviceID != "Vulkan1" || devices[2].Name != "AMD Radeon 780M" || devices[2].VramFreeMiB != 3584 {
		t.Fatalf("expected integrated GPU ordered after discrete GPUs, got %+v", devices[2])
	}

	noGPUErr := hardware.ErrNoVulkanGPU()
	if !hasCode(noGPUErr, derr.CodeNoVulkanGPU) || derr.StatusOf(noGPUErr) != 422 {
		t.Fatalf("expected 422 CodeNoVulkanGPU from ErrNoVulkanGPU, got %v", noGPUErr)
	}
}

func TestDeterministicMachineIDDerivationFromNormalizedHardwareSeed(t *testing.T) {
	idUpper := nodecore.ComputeMachineID("gpu-grid-v1", strings.ToLower("NODE-HOST|00:11:22:33:44:55"))
	idLower := nodecore.ComputeMachineID("gpu-grid-v1", "node-host|00:11:22:33:44:55")
	idOther := nodecore.ComputeMachineID("gpu-grid-v1", "node-host|aa:bb:cc:dd:ee:ff")

	if !strings.HasPrefix(idUpper.String(), "m-") || idUpper != idLower {
		t.Fatalf("expected deterministic m- machine fingerprint, upper=%q lower=%q", idUpper, idLower)
	}
	if idUpper == idOther {
		t.Fatalf("expected distinct NIC MAC seeds to yield distinct machine IDs, got %q", idUpper)
	}
}
