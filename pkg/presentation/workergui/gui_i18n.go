package workergui

type localeOption struct {
	code  string
	label string
}

var supportedLocaleOptions = []localeOption{
	{code: "pt-BR", label: "Português"},
	{code: "en", label: "English"},
}

func LocaleOptionLabels() []string {
	out := make([]string, len(supportedLocaleOptions))
	for i, opt := range supportedLocaleOptions {
		out[i] = opt.label
	}
	return out
}

func LocaleIndexForCode(code string) int {
	if code == "pt-BR" {
		return 0
	}
	return 1
}

func LocaleCodeForIndex(idx int) string {
	if idx >= 0 && idx < len(supportedLocaleOptions) {
		return supportedLocaleOptions[idx].code
	}
	return "en"
}

type GUILabels struct {
	WindowTitle           string
	LangLabel             string
	ModeSimpleBtn         string
	ModeAdvancedBtn       string
	AdvTabSettings        string
	AdvTabDebug           string
	SimpleLoginTitle      string
	SimpleLoginSub        string
	SimpleLoginPendingSub string
	SimpleLoginBtn        string
	SimpleReopenBtn       string
	SimpleCancelLoginBtn  string
	SimpleSwitchAcct      string
	SimpleWithdrawBtn     string
	SimpleStatusTitle     string
	SimpleTokensTitle     string
	SimpleEarnedTitle     string
	SimpleHostModeLbl     string
	SimpleModeAutoBtn     string
	SimpleModeChooseBtn   string
	SimpleAutoInfo        string
	SimpleChooseInfo      string
	SimpleStartBtn        string
	SimpleStartingBtn     string
	SimplePauseBtn        string
	SimplePausingBtn      string
	SimpleResumeBtn       string
	SimpleDisconnectBtn   string
	CopyLogsBtn           string
	LogsCopiedBtn         string
	ClearLogsBtn          string
	BoxTitle              string
	Gateway               string
	Token                 string
	Machine               string
	SmartIdle             string
	IdleSec               string
	PauseOffer            string
	PausingOffer          string
	PauseDrainingBtn      string
	AutoPausedBtn         string
	ResumeOffer           string
	GPUsLabel             string
	ScanGPUs              string
	Scanning              string
	NoGPUs                string
	ModelHubLabel         string
	ModeAuto              string
	ModeMulti             string
	RefreshModels         string
	ApplyModels           string
	NoModels              string
	FitSolo               string
	FitMesh               string
	FitInsufficientVRAM   string
	FitInsufficientDisk   string
	StartBtn              string
	StopBtn               string
	KPIStatus             string
	KPIModel              string
	KPIClearing           string
	KPIBalance            string
	LogsBox               string
	InvalidIdleErr        string
	PausedBanner          string
	PauseDrainingBanner   string
	QuitTitle             string
	QuitMsg               string
	QuitDetail            string
}

var labelsPT = GUILabels{
	WindowTitle:           "GPU Grid Worker",
	LangLabel:             "Idioma:",
	ModeSimpleBtn:         "Modo Simples",
	ModeAdvancedBtn:       "Modo Avançado",
	AdvTabSettings:        "Configurações",
	AdvTabDebug:           "Debug / Logs",
	SimpleLoginTitle:      "Conecte sua Conta GPU Grid",
	SimpleLoginSub:        "Clique abaixo para abrir seu navegador padrão, autorizar o acesso e configurar o Worker automaticamente.",
	SimpleLoginPendingSub: "Aguardando autorização no seu navegador padrão. Confirme o acesso na página aberta para concluir.",
	SimpleLoginBtn:        "ENTRAR COM O NAVEGADOR",
	SimpleReopenBtn:       "REABRIR NAVEGADOR",
	SimpleCancelLoginBtn:  "Cancelar",
	SimpleSwitchAcct:      "Trocar Conta",
	SimpleWithdrawBtn:     "Sacar Saldo ↗",
	SimpleStatusTitle:     "STATUS DO WORKER",
	SimpleTokensTitle:     "PROCESSAMENTO FEITO",
	SimpleEarnedTitle:     "GANHOS ACUMULADOS",
	SimpleHostModeLbl:     "Modelos Hospedados:",
	SimpleModeAutoBtn:     "Automático",
	SimpleModeChooseBtn:   "Escolher Modelos",
	SimpleAutoInfo:        "Automático: modelos ocultos · seleção e download sob demanda pela plataforma · auto-pausa ao usar o PC",
	SimpleChooseInfo:      "Marque os modelos que deseja hospedar (pré-download automático ao iniciar):",
	SimpleStartBtn:        "▶ INICIAR COMPARTILHAMENTO",
	SimpleStartingBtn:     "PREPARANDO E INICIANDO...",
	SimplePauseBtn:        "⏸ PAUSAR COMPARTILHAMENTO",
	SimplePausingBtn:      "DRENANDO PARA PAUSA...",
	SimpleResumeBtn:       "▶ RETOMAR COMPARTILHAMENTO",
	SimpleDisconnectBtn:   "Desconectar",
	CopyLogsBtn:           "Copiar Logs",
	LogsCopiedBtn:         "Copiado!",
	ClearLogsBtn:          "Limpar Logs",
	BoxTitle:              " Conexão, GPUs e Hub de Modelos ",
	Gateway:               "Gateway Central:",
	Token:                 "Worker Token (wk_...):",
	Machine:               "Nome da Máquina:",
	SmartIdle:             "Auto-Pause Inteligente (ao usar PC)",
	IdleSec:               "Ocioso (s):",
	PauseOffer:            "Pausar Oferta",
	PausingOffer:          "Drenando...",
	PauseDrainingBtn:      "Retomar (Drenando)",
	AutoPausedBtn:         "Pausar Manualmente",
	ResumeOffer:           "Retomar Oferta",
	GPUsLabel:             "GPUs Vulkan (marque quais dedicar):",
	ScanGPUs:              "Detectar GPUs",
	Scanning:              "Detectando...",
	NoGPUs:                "Nenhuma GPU detectada. Clique em Detectar GPUs.",
	ModelHubLabel:         "Hub de Modelos (Auto / Seleção):",
	ModeAuto:              "AUTO (Oculto)",
	ModeMulti:             "ESCOLHER MODELOS",
	RefreshModels:         "Atualizar Modelos",
	ApplyModels:           "Aplicar Modelos",
	NoModels:              "Clique em Atualizar Modelos para listar o catálogo do Gateway.",
	FitSolo:               "SOLO",
	FitMesh:               "FILA MESH",
	FitInsufficientVRAM:   "VRAM INSUF.",
	FitInsufficientDisk:   "DISCO INSUF.",
	StartBtn:              "INICIAR WORKER (CONECTAR À REDE)",
	StopBtn:               "PARAR WORKER",
	KPIStatus:             "STATUS / CONFIAB.",
	KPIModel:              "MODELO ATIVO / ALVO",
	KPIClearing:           "TARIFA / CAMADAS",
	KPIBalance:            "SALDO ACUMULADO",
	LogsBox:               " Telemetria e Logs em Tempo Real ",
	InvalidIdleErr:        "Informe segundos ociosos entre 15 e 3600",
	PausedBanner:          "OFERTA PAUSADA — 0 MiB de VRAM em uso (%s)",
	PauseDrainingBanner:   "DRENANDO PARA PAUSA — aguardando término das tarefas ativas (%s)",
	QuitTitle:             "Encerrar GPU Grid Worker",
	QuitMsg:               "O Worker está ativo na rede.",
	QuitDetail:            "Deseja parar o aluguel da GPU e fechar o aplicativo?",
}

var labelsEN = GUILabels{
	WindowTitle:           "GPU Grid Worker",
	LangLabel:             "Language:",
	ModeSimpleBtn:         "Simple Mode",
	ModeAdvancedBtn:       "Advanced Mode",
	AdvTabSettings:        "Settings",
	AdvTabDebug:           "Debug / Logs",
	SimpleLoginTitle:      "Connect Your GPU Grid Account",
	SimpleLoginSub:        "Click below to open your default browser, authorize access, and configure your Worker automatically.",
	SimpleLoginPendingSub: "Waiting for authorization in your default browser. Approve access on the opened page to finish.",
	SimpleLoginBtn:        "SIGN IN WITH BROWSER",
	SimpleReopenBtn:       "REOPEN BROWSER",
	SimpleCancelLoginBtn:  "Cancel",
	SimpleSwitchAcct:      "Switch Account",
	SimpleWithdrawBtn:     "Withdraw ↗",
	SimpleStatusTitle:     "WORKER STATUS",
	SimpleTokensTitle:     "COMPUTE PROCESSED",
	SimpleEarnedTitle:     "TOTAL EARNINGS",
	SimpleHostModeLbl:     "Hosted Models:",
	SimpleModeAutoBtn:     "Automatic",
	SimpleModeChooseBtn:   "Choose Models",
	SimpleAutoInfo:        "Automatic: models hidden · platform selects and pulls on demand · auto-pauses while using PC",
	SimpleChooseInfo:      "Select the models you want to host (automatically pre-downloaded on start):",
	SimpleStartBtn:        "▶ START GPU SHARING",
	SimpleStartingBtn:     "PREPARING & STARTING...",
	SimplePauseBtn:        "⏸ PAUSE SHARING",
	SimplePausingBtn:      "DRAINING TO PAUSE...",
	SimpleResumeBtn:       "▶ RESUME SHARING",
	SimpleDisconnectBtn:   "Disconnect",
	CopyLogsBtn:           "Copy Logs",
	LogsCopiedBtn:         "Copied!",
	ClearLogsBtn:          "Clear Logs",
	BoxTitle:              " Connection, GPUs & Model Hub ",
	Gateway:               "Gateway URL:",
	Token:                 "Worker Token (wk_...):",
	Machine:               "Machine Label:",
	SmartIdle:             "Smart Auto-Pause (while using PC)",
	IdleSec:               "Idle (s):",
	PauseOffer:            "Pause Offer",
	PausingOffer:          "Draining...",
	PauseDrainingBtn:      "Resume (Draining)",
	AutoPausedBtn:         "Pause Manually",
	ResumeOffer:           "Resume Offer",
	GPUsLabel:             "Vulkan GPUs (select devices to rent):",
	ScanGPUs:              "Scan GPUs",
	Scanning:              "Scanning...",
	NoGPUs:                "No Vulkan GPU detected. Click Scan GPUs.",
	ModelHubLabel:         "Model Hub (Auto / Multi):",
	ModeAuto:              "AUTO",
	ModeMulti:             "CHOOSE MODELS",
	RefreshModels:         "Refresh Models",
	ApplyModels:           "Apply Models",
	NoModels:              "Click Refresh Models to fetch the Gateway catalog.",
	FitSolo:               "SOLO",
	FitMesh:               "MESH QUEUE",
	FitInsufficientVRAM:   "INSUF. VRAM",
	FitInsufficientDisk:   "INSUF. DISK",
	StartBtn:              "START WORKER (CONNECT TO GRID)",
	StopBtn:               "STOP WORKER",
	KPIStatus:             "STATUS / RELIAB.",
	KPIModel:              "ACTIVE / TARGET MODEL",
	KPIClearing:           "RATE / LAYERS",
	KPIBalance:            "EARNED BALANCE",
	LogsBox:               " Real-Time Telemetry & Logs ",
	InvalidIdleErr:        "Enter idle seconds between 15 and 3600",
	PausedBanner:          "OFFER PAUSED — 0 MiB VRAM in use (%s)",
	PauseDrainingBanner:   "DRAINING TO PAUSE — waiting for active jobs to finish (%s)",
	QuitTitle:             "Quit GPU Grid Worker",
	QuitMsg:               "Worker is currently active.",
	QuitDetail:            "Stop GPU rental and close the application?",
}

func LabelsFor(lang string) GUILabels {
	if lang == "en" {
		return labelsEN
	}
	return labelsPT
}
