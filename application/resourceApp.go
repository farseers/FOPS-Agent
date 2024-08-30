// @area /api/
package application

import (
	"github.com/farseer-go/utils/system"
)

// 获取宿主资源使用情况
// @get /host/resource
func Resource() system.Resource {
	return system.GetResource()
}
