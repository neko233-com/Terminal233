package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// Gauges complement the textual values and disappear when a sample is stale.
func (d *desktop) monitorGauges(c *ui.Context) {
	gauges := []struct {
		name    string
		value   float64
		present bool
	}{
		{"CPU", d.monitorData.cpu, d.monitorData.hasCPU},
		{"内存", d.monitorData.memory, d.monitorData.hasMemory},
		{"根磁盘", d.monitorData.disk, d.monitorData.hasDisk},
	}
	ui.Row(c).FillWidth().Gap(12).Children(func() {
		for _, gauge := range gauges {
			if !gauge.present {
				continue
			}
			gauge := gauge
			ui.Column(c).Grow(1).Gap(3).Children(func() {
				label := fmt.Sprintf("%s 使用率 %.1f%%", gauge.name, gauge.value*100)
				ui.Text(c, label).FontSize(11)
				ui.Progress(c.Key("monitor-gauge-"+gauge.name), gauge.value).Label(label).FillWidth()
			})
		}
	})
}
