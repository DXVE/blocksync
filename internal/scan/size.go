// 字节数的显示格式：命令行与镜像日志共用同一实现，避免两侧格式漂移。
package scan

import "fmt"

// FormatSize 把字节数格式化为带单位的短字符串（B/KiB/MiB/GiB/TiB）。
func FormatSize(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	v := float64(n)
	i := -1
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}
