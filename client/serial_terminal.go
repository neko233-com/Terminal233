package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
	"go.bug.st/serial"
)

// Serial settings are device-local. They never contain SSH credentials.
type serialSettings struct {
	Port     string `json:"port"`
	BaudRate int    `json:"baud_rate"`
	DataBits int    `json:"data_bits"`
	Parity   string `json:"parity"`
	StopBits string `json:"stop_bits"`
	DTR      bool   `json:"dtr,omitempty"`
	RTS      bool   `json:"rts,omitempty"`
}

type serialDraft struct {
	Port, BaudRate, DataBits, Parity, StopBits string
	DTR, RTS                                   bool
}

func defaultSerialDraft() serialDraft {
	return serialDraft{BaudRate: "115200", DataBits: "8", Parity: "None", StopBits: "1"}
}

func (draft serialDraft) settings() (serialSettings, error) {
	baud, err := strconv.Atoi(strings.TrimSpace(draft.BaudRate))
	if err != nil {
		return serialSettings{}, errors.New("波特率必须是数字")
	}
	bits, err := strconv.Atoi(draft.DataBits)
	if err != nil {
		return serialSettings{}, errors.New("数据位无效")
	}
	s := serialSettings{Port: strings.ToUpper(strings.TrimSpace(draft.Port)), BaudRate: baud, DataBits: bits, Parity: draft.Parity, StopBits: draft.StopBits, DTR: draft.DTR, RTS: draft.RTS}
	if err := s.validate(); err != nil {
		return serialSettings{}, err
	}
	return s, nil
}

func (s serialSettings) validate() error {
	if !strings.HasPrefix(s.Port, "COM") {
		return errors.New("请输入 Windows 串口名称，例如 COM3")
	}
	port, err := strconv.Atoi(strings.TrimPrefix(s.Port, "COM"))
	if err != nil || port < 1 || port > 65535 || s.Port != fmt.Sprintf("COM%d", port) {
		return errors.New("串口名称无效，请使用 COM 加端口号")
	}
	if s.BaudRate < 50 || s.BaudRate > 4000000 || s.DataBits < 5 || s.DataBits > 8 {
		return errors.New("波特率或数据位超出支持范围")
	}
	switch s.Parity {
	case "None", "Odd", "Even", "Mark", "Space":
	default:
		return errors.New("校验位无效")
	}
	switch s.StopBits {
	case "1":
	case "1.5":
		if s.DataBits != 5 {
			return errors.New("1.5 停止位需要 5 数据位")
		}
	case "2":
		if s.DataBits == 5 {
			return errors.New("5 数据位不支持 2 停止位")
		}
	default:
		return errors.New("停止位无效")
	}
	return nil
}

func (s serialSettings) mode() (*serial.Mode, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	parity := map[string]serial.Parity{"None": serial.NoParity, "Odd": serial.OddParity, "Even": serial.EvenParity, "Mark": serial.MarkParity, "Space": serial.SpaceParity}[s.Parity]
	stop := map[string]serial.StopBits{"1": serial.OneStopBit, "1.5": serial.OnePointFiveStopBits, "2": serial.TwoStopBits}[s.StopBits]
	return &serial.Mode{BaudRate: s.BaudRate, DataBits: s.DataBits, Parity: parity, StopBits: stop, InitialStatusBits: &serial.ModemOutputBits{DTR: s.DTR, RTS: s.RTS}}, nil
}

func serialOpenError(port string, err error) error {
	var portErr *serial.PortError
	if errors.As(err, &portErr) {
		message := ""
		switch portErr.Code() {
		case serial.PortBusy:
			message = "设备已被其他程序占用"
		case serial.PortNotFound, serial.InvalidSerialPort:
			message = "没有找到串口设备"
		case serial.PermissionDenied:
			message = "没有访问权限"
		case serial.InvalidSpeed, serial.InvalidDataBits, serial.InvalidParity, serial.InvalidStopBits:
			message = "设备不支持这些串口参数"
		}
		if message != "" {
			return fmt.Errorf("无法打开 %s：%s", port, message)
		}
	}
	return fmt.Errorf("无法打开串口 %s：%w", port, err)
}

func openSerialTerminal(ctx context.Context, settings serialSettings, options terminal.Options) (*terminal.Terminal, error) {
	mode, err := settings.mode()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	port, err := serial.Open(settings.Port, mode)
	if err != nil {
		return nil, serialOpenError(settings.Port, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = port.Close() })
	defer stop()
	options.Conn = port
	term, err := terminal.New(options)
	if err != nil {
		_ = port.Close()
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		_ = term.Close()
		return nil, err
	}
	return term, nil
}

func (d *desktop) currentSerialSession() *workspaceSession {
	for _, s := range d.sessions {
		if s.id == d.currentSession && s.serial != nil {
			return s
		}
	}
	return nil
}

func (d *desktop) refreshSerialPorts() {
	if d.win == nil || d.serialPortLoading {
		return
	}
	d.serialPortLoading, d.serialPortStatus = true, "正在查找串口…"
	win := d.win
	go func() {
		ports, err := serial.GetPortsList()
		sort.Strings(ports)
		win.Update(func() {
			d.serialPortLoading = false
			if err != nil {
				d.serialPorts, d.serialPortStatus = nil, "无法读取串口列表："+err.Error()
				return
			}
			d.serialPorts = ports
			d.serialPortStatus = ""
			if len(ports) == 0 {
				d.serialPortStatus = "没有发现串口，可手动输入端口名称。"
			} else if d.serialDraft.Port == "" {
				d.serialDraft.Port = ports[0]
			}
		})
	}()
}

func (d *desktop) startSerialSession() {
	if d.busy || d.win == nil || d.terminalLimitReached() {
		return
	}
	settings, err := d.serialDraft.settings()
	if err != nil {
		d.newTabStatus = err.Error()
		return
	}
	d.busy, d.newTabStatus = true, "正在打开串口…"
	options, win := d.terminalOptions(), d.win
	go func() {
		term, err := openSerialTerminal(context.Background(), settings, options)
		win.Update(func() {
			d.busy = false
			if err != nil {
				d.newTabStatus = err.Error()
				return
			}
			if d.workspace != "new-tab" || !d.showSerialSetup || d.terminalLimitReached() {
				_ = term.Close()
				return
			}
			d.showSerialSetup, d.newTabOpen, d.newTabQuery = false, false, ""
			d.addSessionWithSerial(term, nil, connectionSettings{}, "串口 · "+settings.Port, &settings)
			d.status = fmt.Sprintf("串口 %s · %d 波特", settings.Port, settings.BaudRate)
		})
	}()
}

func (d *desktop) reconnectSerialSession(s *workspaceSession) {
	if d.busy || d.win == nil || s == nil || s.serial == nil || !terminalEnded(s.term) {
		return
	}
	settings, old, options, win := *s.serial, s.term, d.terminalOptions(), d.win
	d.busy, d.status = true, "正在重新连接串口…"
	go func() {
		term, err := openSerialTerminal(context.Background(), settings, options)
		win.Update(func() {
			d.busy = false
			if err != nil {
				d.status = err.Error()
				return
			}
			for _, current := range d.sessions {
				if current != s || current.term != old || current.serial == nil || *current.serial != settings {
					continue
				}
				_ = old.Close()
				term.SetFont(terminal.Font{Size: float32(d.currentTerminalFontSize())})
				term.SetTheme(d.terminalLightTheme, d.terminalDarkTheme)
				s.term, s.terminalSearchOpen, s.terminalSearchText = term, false, ""
				if group := d.terminalGroupFor(s.id); group != nil {
					group.broadcast, group.broadcastMembers = false, nil
				}
				d.bindTerminalBroadcast(s)
				if d.currentSession == s.id {
					d.term, d.terminalSearchOpen, d.terminalSearchText = term, false, ""
					d.status = "串口已重新连接"
				}
				return
			}
			_ = term.Close()
		})
	}()
}

func (d *desktop) serialSetupView(c *ui.Context) {
	ui.Column(c.Key("serial-setup")).FillWidth().Padding(16).Gap(10).Background(c.Theme().Surface).Radius(10).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, "串口连接").FontSize(15).Bold().Grow(1)
			if ui.Button(c.Key("serial-close"), "取消").Disabled(d.busy).Clicked() {
				d.showSerialSetup = false
			}
		})
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.TextInput(c.Key("serial-port"), &d.serialDraft.Port).Label("串口名称").Placeholder("COM3").Grow(1)
			if len(d.serialPorts) > 0 {
				ui.Select(c.Key("serial-port-list"), &d.serialDraft.Port, d.serialPorts).Label("可用串口").Width(130)
			}
			if ui.Button(c.Key("serial-refresh"), "刷新").Disabled(d.serialPortLoading || d.busy).Clicked() {
				d.refreshSerialPorts()
			}
		})
		if d.serialPortStatus != "" {
			ui.Text(c, d.serialPortStatus).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
		}
		ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Width(110).Gap(4).Children(func() {
				ui.Text(c, "波特率").FontSize(12).TextColor(c.Theme().TextMuted)
				ui.TextInput(c.Key("serial-baud"), &d.serialDraft.BaudRate).Label("波特率").FillWidth()
			})
			ui.Column(c).Width(80).Gap(4).Children(func() {
				ui.Text(c, "数据位").FontSize(12).TextColor(c.Theme().TextMuted)
				ui.Select(c.Key("serial-data"), &d.serialDraft.DataBits, []string{"5", "6", "7", "8"}).Label("数据位").FillWidth()
			})
			ui.Column(c).Width(110).Gap(4).Children(func() {
				ui.Text(c, "校验位").FontSize(12).TextColor(c.Theme().TextMuted)
				ui.Select(c.Key("serial-parity"), &d.serialDraft.Parity, []string{"None", "Odd", "Even", "Mark", "Space"}).Label("校验位").FillWidth()
			})
			ui.Column(c).Width(80).Gap(4).Children(func() {
				ui.Text(c, "停止位").FontSize(12).TextColor(c.Theme().TextMuted)
				ui.Select(c.Key("serial-stop"), &d.serialDraft.StopBits, []string{"1", "1.5", "2"}).Label("停止位").FillWidth()
			})
		})
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Checkbox(c.Key("serial-dtr"), &d.serialDraft.DTR, "DTR").Label("连接时启用 DTR").Tooltip("部分设备会响应 DTR 信号并复位")
			ui.Checkbox(c.Key("serial-rts"), &d.serialDraft.RTS, "RTS").Label("连接时启用 RTS").Tooltip("设置连接时的 RTS 电平")
			ui.Text(c, "无流控").FontSize(12).TextColor(c.Theme().TextMuted)
			ui.Box(c).Grow(1)
			if ui.PrimaryButton(c.Key("serial-connect"), "连接串口").Disabled(d.busy || d.terminalLimitReached()).Clicked() {
				d.startSerialSession()
			}
		})
	})
}
