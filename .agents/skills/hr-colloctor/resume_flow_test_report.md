# 简历监控与初筛流程测试报告

## 📊 测试概况

| 项目 | 结果 |
|------|------|
| Wiki 问卷记录数 | 160 条 |
| 目标表已有记录 | 30 条 |
| 测试岗位候选人 | 25 个 |
| **新候选人** | **2 个** |
| 数据读取 | ✅ 成功 |
| 重复检测 | ✅ 成功 |
| 自动评分 | ✅ 成功 |
| 写入记录 | ❌ 403 权限不足 |

---

## 🔍 发现的问题与优化建议

### 1. 【权限配置】🔴 高优先级
**问题**: `HTTP 403: you don't have permission`  
当前 OAuth 授权缺少写入目标多维表格的权限。

**建议**:
- 检查飞书应用权限：需要开启 `bitable:record` 写权限
- 或者使用拥有表格编辑权限的个人访问令牌
- 确认应用已被邀请进入目标 Base

---

### 2. 【数据获取优化】🟡 中优先级
**问题**: 
- 当前一次性拉取全部 160 条记录，数据量大时可能超时
- 没有增量同步机制

**建议**:
```python
# 优化 1: 使用分页拉取
offset = 0
limit = 100
all_records = []
while True:
    result = lark_cli(..., offset=offset, limit=limit)
    all_records.extend(result)
    if not result.get("has_more"):
        break
    offset += limit

# 优化 2: 增量同步（只拉最近 N 天）
# 利用 "提交时间" 字段筛选最近 7 天的记录
```

---

### 3. 【评分逻辑优化】🟡 中优先级
**问题**: 当前规则评分较为简单，可能有误判

**当前评分规则**:
| 维度 | 满分 | 当前规则 |
|------|------|----------|
| 基础背景 | 20 | 测试经验等级 + 自动化经验 |
| 核心技能 | 30 | AI产品经验 + CherryStudio熟悉度 |
| 加分项 | 25 | 开源项目 + AI工具使用 |
| 综合素质 | 30 | 留言长度 |

**建议改进**:
1. **简历内容分析**: 下载 PDF 后提取文本，用 LLM 分析
2. **关键词匹配**: 检查是否包含 "Python/Selenium/Appium/Charles" 等技能词
3. **经验年限加权**: 结合 "经验年限" 字段计算
4. **项目质量评估**: 分析 GitHub 链接的项目活跃度

```python
# 示例：LLM 辅助评分
resume_text = extract_pdf_text(resume_file)
prompt = f"""
作为全端测试工程师招聘专家，请根据简历内容评分（满分100）：
- 基础背景(20): 学历、经验
- 核心技能(30): 多端测试、自动化、抓包
- 加分项(25): AI测试、大厂、工具使用
- 综合素质(30): 逻辑、学习敏锐度

简历内容：{resume_text}

返回 JSON 格式：{{"score": 85, "reason": "..."}}
"""
```

---

### 4. 【简历处理流程】🟡 中优先级
**问题**: 当前未实现简历下载和上传

**建议完整流程**:
```python
# Step 1: 下载简历
lark-cli drive +media-download \
  --file-token "TrhqbBabio1Hxcxe5e8cgg3VnJh" \
  --output "/tmp/邢凯宁_简历.pdf"

# Step 2: 提取简历文本（可选）
text = extract_text("/tmp/邢凯宁_简历.pdf")

# Step 3: 创建记录
record_id = create_record(...)  # 先创建记录

# Step 4: 上传附件到记录
lark-cli base +record-upload-attachment \
  --base-token "B7oybOgfZanNdMsPVU3cD6MHnlS" \
  --table-id "tblozCB8F2NqWiQ4" \
  --record-id "recxxxx" \
  --field-id "fldaYhtrZz" \
  --file-path "/tmp/邢凯宁_简历.pdf"
```

---

### 5. 【通知机制】🟢 低优先级
**问题**: 任务要求发送通知，但未实现

**建议方案**:
```python
# 方案 1: 使用 notify 工具
notify.send(
    message=f"🎉 发现新候选人：{name} (编号:{id})\n"
            f"📊 评分：{score} 分\n"
            f"✨ 亮点：{highlights}"
)

# 方案 2: 飞书群机器人
lark-cli im +send-message \
  --receive-id "oc_xxxxx" \
  --content '{"text":"新候选人通知..."}'
```

---

### 6. 【字段映射校准】🟢 低优先级
**Wiki 问卷字段** -> **目标表字段**

| Wiki 字段 | 目标表字段 | 说明 |
|-----------|-----------|------|
| 编号 | 投递编号 | ✅ 直接映射 |
| 你的姓名 | 候选人姓名 | ✅ 直接映射 |
| 你的联系方式 | 联系方式 | ✅ 直接映射 |
| 你的目标岗位 | 应聘岗位 | 需转换文本 |
| 上传简历 | 简历附件 | 需下载再上传 |
| 提交时间 | (可添加) | 用于增量同步 |

**缺失字段补充建议**:
- 年龄：可添加字段，从简历解析
- 经验年限：可添加字段，从简历解析
- 技术栈：可添加字段，从简历解析

---

### 7. 【去重逻辑完善】🟢 低优先级
**当前逻辑**: 检查 "投递编号" 是否已存在  
**潜在问题**: 
- 同一候选人可能用不同邮箱多次投递
- 同名不同人的情况

**建议**:
```python
def is_duplicate(candidate, existing_records):
    candidate_id = str(candidate.get("编号", ""))
    name = str(candidate.get("你的姓名", ""))
    contact = str(candidate.get("你的联系方式", ""))
    
    for record in existing_records:
        # 编号完全匹配
        if record.get("投递编号") == candidate_id:
            return True
        # 姓名+联系方式匹配（同一候选人换编号）
        if (record.get("候选人姓名") == name and 
            record.get("联系方式") == contact):
            return True
    return False
```

---

### 8. 【错误处理与日志】🟢 低优先级
**建议增加**:
- 详细的操作日志记录
- 失败重试机制（特别是网络错误）
- 异常通知（流程失败时告警）

---

## 📝 推荐实现方案

### 方案 A: 简单定时任务（推荐）
```bash
# 每小时执行一次
0 * * * * cd /workspace && python3 resume_monitor.py >> /var/log/resume_monitor.log 2>&1
```

### 方案 B: 使用 scheduler skill
```
stella scheduler add --name "简历监控" --interval "1h" --command "python3 /workspace/resume_monitor.py"
```

### 方案 C: Webhook 实时触发（高级）
- 配置飞书表单提交时触发 Webhook
- 实时处理新投递，无延迟

---

## ✅ 下一步行动清单

- [ ] 解决 Base 写入权限问题
- [ ] 完善简历下载和上传功能
- [ ] 添加 LLM 辅助评分
- [ ] 添加通知功能
- [ ] 配置定时调度
- [ ] 添加日志和监控
- [ ] 测试完整流程

---

## 📌 关键 API 调用示例

```bash
# 1. 获取 Wiki 数据
lark-cli base +record-list \
  --base-token "N9bvbIrdQarFq0sLjkgcmMVfnTb" \
  --table-id "tblK0q9RpL0B75rR" \
  --format json --limit 500

# 2. 获取已处理编号
lark-cli base +record-list \
  --base-token "B7oybOgfZanNdMsPVU3cD6MHnlS" \
  --table-id "tblozCB8F2NqWiQ4" \
  --field-id "投递编号"

# 3. 创建记录
lark-cli base +record-batch-create \
  --base-token "B7oybOgfZanNdMsPVU3cD6MHnlS" \
  --table-id "tblozCB8F2NqWiQ4" \
  --json '{"fields":["投递编号","候选人姓名"],"rows":[["172","张三"]]}'

# 4. 上传附件
lark-cli base +record-upload-attachment \
  --base-token "B7oybOgfZanNdMsPVU3cD6MHnlS" \
  --table-id "tblozCB8F2NqWiQ4" \
  --record-id "recxxxx" \
  --field-id "fldaYhtrZz" \
  --file-path "/path/to/resume.pdf"
```

---

*测试时间: 2025-01-07*  
*测试工具: lark-cli + Python*  
*数据范围: Wiki 160条 / 目标表 30条*
