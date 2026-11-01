package winapi

import (
	"log"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Processos filhos (FFmpeg): sem janela, prioridade alta e presos a um "job object"
// que os encerra junto com o app, mesmo se o app fechar à força.

var job windows.Handle

// InitJob cria o job object. Chamar uma vez no início do programa.
func InitJob() {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err == nil {
		job = h
	}
}

// HideWindow faz o processo rodar sem abrir janela de console.
func HideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// AdoptChild coloca um processo filho no job object e eleva sua prioridade de CPU e GPU.
func AdoptChild(p *os.Process) {
	// acesso total: a API de prioridade de GPU devolve "parâmetro inválido" com menos direitos
	h, err := windows.OpenProcess(windows.PROCESS_ALL_ACCESS, false, uint32(p.Pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	gpuOK := setGPUPriority(h)
	if job != 0 {
		windows.AssignProcessToJobObject(job, h)
	}
	windows.SetPriorityClass(h, windows.HIGH_PRIORITY_CLASS)
	if gpuOK {
		log.Printf("prioridade de GPU elevada (processo %d)", p.Pid)
		return
	}
	go func() { // logo após a criação o processo ainda pode não aceitar: tenta de novo
		for i := 1; i <= 20; i++ {
			time.Sleep(250 * time.Millisecond)
			h, err := windows.OpenProcess(windows.PROCESS_ALL_ACCESS, false, uint32(p.Pid))
			if err != nil {
				return
			}
			ok := setGPUPriority(h)
			windows.CloseHandle(h)
			if ok {
				log.Printf("prioridade de GPU elevada (processo %d, tentativa %d)", p.Pid, i)
				return
			}
		}
		log.Printf("não foi possível elevar a prioridade de GPU (processo %d)", p.Pid)
	}()
}

var procSetGPUPriority = windows.NewLazySystemDLL("gdi32.dll").NewProc("D3DKMTSetProcessSchedulingPriorityClass")

// setGPUPriority dá à captura prioridade no agendador da GPU, para um jogo usando
// 100% da GPU não atrasar a captura dos quadros (mesma técnica do OBS).
// Tenta REALTIME (aceito sem administrador nos testes) e, se recusado, HIGH.
func setGPUPriority(h windows.Handle) bool {
	for _, class := range []uintptr{5, 4} { // REALTIME, HIGH
		if r, _, _ := procSetGPUPriority.Call(uintptr(h), class); r == 0 { // STATUS_SUCCESS
			return true
		}
	}
	return false
}

// ProcessCPUTime devolve o tempo de CPU (usuário + kernel) gasto pelo próprio app.
func ProcessCPUTime() time.Duration {
	var c, e, k, u windows.Filetime
	if windows.GetProcessTimes(windows.CurrentProcess(), &c, &e, &k, &u) != nil {
		return 0
	}
	return time.Duration(filetime(k)+filetime(u)) * 100
}

// ChildrenCPUTime devolve o tempo de CPU somado de todos os processos filhos
// que já passaram pelo job object (FFmpeg).
func ChildrenCPUTime() time.Duration {
	if job == 0 {
		return 0
	}
	var info struct {
		TotalUserTime, TotalKernelTime                     int64
		ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
		TotalPageFaultCount, TotalProcesses                uint32
		ActiveProcesses, TotalTerminatedProcesses          uint32
	}
	if windows.QueryInformationJobObject(job, 1, /* JobObjectBasicAccountingInformation */
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil) != nil {
		return 0
	}
	return time.Duration(info.TotalUserTime+info.TotalKernelTime) * 100
}

func filetime(f windows.Filetime) int64 { return int64(f.HighDateTime)<<32 | int64(f.LowDateTime) }
