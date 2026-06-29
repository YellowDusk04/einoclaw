# EINOCLAW

![Stars](https://img.shields.io/github/stars/YellowDusk04/einoclaw)
[![License](https://img.shields.io/github/license/YellowDusk04/einoclaw)](https://github.com/YellowDusk04/einoclaw/blob/main/LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/YellowDusk04/einoclaw)](https://goreportcard.com/report/github.com/YellowDusk04/einoclaw)

基于 [Eino](https://github.com/cloudwego/eino) 框架构建的 **CLI Code Agent**，深度集成 **Eino** 框架的 **TurnLoop** 组件，**Agent** 相关核心代码仅约 **600** 行。

## 🎬 效果展示

### 1. 创建 Demo
使用 **Einoclaw**，基于 Eino 框架快速创建可运行的 demo：

![create-demo](docs/assets/gif/create_demo.gif)

### 2. Markdown 渲染
支持终端渲染 Markdown 格式，提升可读性：

![markdown](docs/assets/gif/markdown.gif)

### 3. 权限审批模式
支持三种人工审批模式，实现 human-in-the-loop：

**允许模式** - 用户批准执行命令
![permission-allow](docs/assets/gif/permission_allow.gif)

**拒绝模式** - 用户拒绝执行命令
![permission-deny](docs/assets/gif/permission_deny.gif)

**响应模式** - 用户拒绝并自定义回复内容
![permission-response](docs/assets/gif/permission_response.gif)

### 4. 会话恢复
支持恢复之前的会话历史，继续未完成的对话：

![resume](docs/assets/gif/resume.gif)

### 5. 用户消息队列
当 AI 正在流式输出时，用户可以继续输入消息，系统会自动排队处理：

![user-message-queue](docs/assets/gif/user_message_queue.gif)

---

## 🚀 快速开始

### 安装

```bash
git clone git@github.com:YellowDusk04/einoclaw.git
cd einoclaw
go run .
```

### 配置

首次运行会自动生成配置文件 `~/.einoclaw/config.yaml`，填入模型配置后重新运行 `go run .`。

**示例配置：**

```yaml
models:
  - model_name: deepseek-v4-flash
    model_id: deepseek-v4-flash
    provider: deepseek
    api_key: sk-xxxxxx
    base_url: https://api.deepseek.com

  - model_name: Qwen3.6-35B-A3B
    model_id: qwen3.6-35b-a3b
    provider: qwen
    api_key: sk-xxxxxx
    base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
    enable_thinking: false
```

### 中间件配置

中间件是否添加以及详细信息可通过配置文件灵活控制，例如：

```yaml
summarization:
  enabled: true
  context_tokens: 60
```

- `enabled=true` 表示启用摘要中间件，改为 `false` 则不启用
- `context_tokens: 60` 表示上下文长度达到 `60k` 时触发自动摘要（单位 `k`）

---

## 🏗️ 核心架构

基于 **[Bubble Tea](https://github.com/charmbracelet/bubbletea)** (v2) 框架构建的事件驱动架构。

### 事件循环

Bubble Tea 的核心是一个事件循环（`Init` → `Update` → `View`），参考 [BubbleTea eventloop函数](https://github.com/charmbracelet/bubbletea/blob/main/tea.go#L741) 的实现。简单来说就是**事件循环收到事件，然后渲染终端**。

1. **主协程（TUI 事件循环）**：运行 Bubble Tea 的 `program.Run()`，负责：
   - 监听各种事件：
     - 键盘输入（`tea.KeyPressMsg`）
     - 鼠标事件（`tea.MouseMsg`：点击、滚轮等）
     - 窗口大小变化（`tea.WindowSizeMsg`）
     - 粘贴事件（`tea.PasteMsg`）
     - Agent 事件（自定义消息类型，如思考分片, 工具调用、工具响应、中断审批等。）
   - 调用 `View()` 渲染终端界面

2. **后台协程（Agent 运行）**：运行 `TurnLoop`，负责：
   - 接收用户输入（`turnLoop.Push()`）
   - 执行 Agent 逻辑（工具调用、模型推理等）
   - 产生**Agent**相关事件并通过 `program.Send()` 发送给 TUI

### 事件流转

```
用户输入
  ↓
turnLoop.Push(chatItem{...})
  ↓
Agent 处理（TurnLoop 后台协程）
  ↓
OnAgentEvents() 解析事件
  ↓
program.Send(自定义消息)  [如 aiTextChunkMsg、toolCallMsg 等]
  ↓
TUI 的 Update() 接收消息
  ↓
更新 teaModel 状态
  ↓
View() 渲染界面
```

---

## 📁 项目结构

| 文件 | 功能 |
|------|------|
| **main.go** | 程序入口，配置并启动 `TurnLoop`，定义 `OnAgentEvents` 回调函数处理 Agent 事件 |
| **tui.go** | Bubble Tea TUI 实现：`teaModel` 定义、事件处理（`Update`）、界面渲染（`View`）、键盘交互 |
| **init.go** | `init()` 函数，负责程序启动前的初始化（配置加载、日志、目录创建等） |
| **handlers.go** | 定义 Eino 中间件的构造函数（filesystem、permission、summarization 等） |
| **trace.go** | 链路追踪相关函数，目前集成 Cozeloop |
| **chatlist.go** | 聊天记录管理：消息列表、滚动、展开/折叠、渲染 |
| **markdown.go** | 终端 Markdown 渲染能力（基于 glamour） |
| **messages.go** | 消息类型定义和处理的辅助函数 |
| **config.go** | 配置结构定义和配置文件加载 |
| **model.go** | 模型配置和加载相关代码 |

**代码统计：**
- 项目总计约 **2200** 行代码
- **Agent 相关代码**约 **600** 行（main.go 中 OnAgentEvents 及 handlers.go）
- **TUI 相关代码**约 **1600** 行（tui.go、chatlist.go、markdown.go、messages.go）

---

## ✨ 功能特性

### 模型支持

支持多种模型提供商，在 `config.yaml` 中配置：

- **Qwen**（通义千问）
- **OpenAI**
- **Ark**（火山引擎）
- **DeepSeek**

### 文件系统操作

通过 `filesystem` middleware 提供工具：

- `ls` - 列出目录内容
- `read_file` - 读取文件
- `write_file` - 写入文件
- `edit_file` - 编辑文件
- `grep` - 搜索文件内容
- `glob` - 匹配文件路径
- `execute` - 执行命令

### 上下文管理

| Middleware | 功能 |
|------------|------|
| `summarization` | 上下文超限时自动总结历史对话 |
| `reduction` | 截断/清理过长消息，控制 token 消耗 |
| `patch_tool_calls` | 修复重复/孤立的工具调用结果 |

### 短期记忆

`~/.einoclaw/sessions` 保存了每一次会话产生的事件，支持会话恢复。

### 长期记忆

`automemory` middleware 从对话中提取记忆，持久化到 `~/.einoclaw/memory`，跨会话检索。

### 权限控制

`permission` middleware 实现 human-in-the-loop：执行命令前询问用户，可配置黑名单，支持三种审批模式（允许、拒绝、自定义响应）。

### Skill

`skill` middleware 加载本地技能目录（`~/.agents/skills`），动态扩展 Agent 能力。

### 可观测性

可选集成 [Cozeloop](https://www.coze.cn/open/docs/coze_loop/overview) 追踪 Agent 执行。

---

## ⚙️ 配置说明

配置文件位置：`~/.einoclaw/config.yaml`

配置文件支持以下主要配置项：

- **models**: 模型配置列表
- **summarization**: 摘要中间件配置
- **reduction**: 消息截断中间件配置
- **automemory**: 自动记忆中间件配置
- **permission**: 权限控制中间件配置
- **skill**: 技能加载中间件配置

---

## 🙏 致谢

感谢以下开源项目：

- [eino](https://github.com/cloudwego/eino) - 提供强大的 **Agent** 应用开发框架
- [Bubble Tea](https://github.com/charmbracelet/bubbletea) - 提供优秀的 TUI 框架

---

## 📄 License

MIT License
