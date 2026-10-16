// Package escl 实现远程复印需要的 eSCL（AirScan）客户端。
//
// # 设计说明
//
// 「复印」= 扫描 + 打印。打印半边复用现有 IPP/CUPS 链路，这里只负责扫描半边。
//
// CUPS 不管扫描仪。eSCL（Apple AirScan，Mopria 公开标准）是一个 HTTP + XML
// 协议，绝大多数 2015 年后的网络一体机原生开放（Ricoh / HP / Canon / Epson /
// Brother / Kyocera …），端点是打印机 80/443 端口下的 /eSCL/* 路径：
//
//	GET    /eSCL/ScannerCapabilities        能力（型号、玻璃板/ADF、双面、色彩、分辨率）
//	GET    /eSCL/ScannerStatus              状态（Idle / Processing / Down、ADF 有无纸）
//	POST   /eSCL/ScanJobs                   创建扫描任务（body 是扫描设置 XML），
//	                                        响应 201 + Location: 任务 URL
//	GET    /eSCL/ScanJobs/{id}              任务状态（JobState: Processing/Completed/…）
//	GET    /eSCL/ScanJobs/{id}/NextDocument 取扫描件（PDF/JPEG）
//	DELETE /eSCL/ScanJobs/{id}              取消/结束任务
//
// 流程：POST 建任务 → 轮询任务状态 → NextDocument 取件 → DELETE 收尾。
//
// # 与打印机的对接注意（Ricoh Pro C5200S 实测）
//
//   - 扫描设置 XML 的命名空间是 scan=…/escl/2011/05/03 与 pwg=…/2010/12/sm，
//     手写 XML 字符串而不是用 encoding/xml 序列化 —— 命名空间声明要挂在根元素上，
//     xml.Marshal 做这个很别扭。
//   - 坐标单位是 1/300 英寸（PWG ThreeHundredthsOfInches）。
//   - ADF 多页：ImagesToTransfer=0（吃到进稿器空为止）+ DocumentFormat=application/pdf，
//     一次 NextDocument 返回整份多页 PDF。
//   - 玻璃板（Platen）一次只出一页。
//
// # 安全注意
//
// eSCL 协议本身没有用户鉴权：同一内网的任何人都能让扫描仪工作。
// 所以「发起扫描」这个动作必须只由用户在应用里点击/支付后来触发，
// 并且前端要提示用户扫完及时取走原件（玻璃板上忘取的文件会被下一个人扫走）。
package escl
