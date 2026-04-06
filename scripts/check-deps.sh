#!/usr/bin/env bash

# Copyright 2026 Arcentra Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# check-deps.sh — 检查 internal/ 各层依赖方向是否符合 DDD + Clean Architecture 规则
#
# 依赖规则（箭头表示"可以依赖"）：
#
#   domain  →  (无，只依赖标准库和 shared/)
#   case    →  domain, shared
#   infra   →  domain, case, shared
#   adapter →  domain, case, shared
#   shared     →  (无 internal/ 依赖，只依赖标准库和顶层 shared/)
#
# 违规定义：
#   domain  不得 import  case / infra / adapter
#   case    不得 import  infra / adapter
#   shared     不得 import  domain / case / infra / adapter
#
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

violations=0
checked=0

# check_no_import <layer_dir> <layer_label> <forbidden_pattern> <forbidden_label>
check_no_import() {
  local layer_dir="$1"
  local layer_label="$2"
  local forbidden_pattern="$3"
  local forbidden_label="$4"

  if [ ! -d "$ROOT/$layer_dir" ]; then
    return
  fi

  while IFS= read -r file; do
    while IFS= read -r line; do
      violations=$((violations + 1))
      echo -e "  ${RED}✗${NC} ${file#"$ROOT/"}  →  imports ${YELLOW}${forbidden_label}${NC}"
      echo -e "    ${line}"
    done < <(grep -n "\".*/${forbidden_pattern}" "$file" 2>/dev/null || true)
  done < <(find "$ROOT/$layer_dir" -name '*.go' -not -name '*_test.go' -not -path '*/testdata/*')
}

echo ""
echo "═══════════════════════════════════════════════════════"
echo " DDD 依赖方向检查  $(date '+%Y-%m-%d %H:%M:%S')"
echo "═══════════════════════════════════════════════════════"
echo ""

# ─── Rule 1: domain 不得 import case / infra / adapter ───
echo -e "${YELLOW}[Rule 1]${NC} domain 不得依赖 case / infra / adapter"
checked=$((checked + 1))
before=$violations
check_no_import "internal/domain" "domain" "internal/case"    "internal/case"
check_no_import "internal/domain" "domain" "internal/infra"   "internal/infra"
check_no_import "internal/domain" "domain" "internal/adapter"  "internal/adapter"
if [ $violations -eq $before ]; then
  echo -e "  ${GREEN}✓${NC} 通过"
fi
echo ""

# ─── Rule 2: case 不得 import infra / adapter ───
echo -e "${YELLOW}[Rule 2]${NC} case 不得依赖 infra / adapter"
checked=$((checked + 1))
before=$violations
check_no_import "internal/case" "case" "internal/infra"   "internal/infra"
check_no_import "internal/case" "case" "internal/adapter"  "internal/adapter"
if [ $violations -eq $before ]; then
  echo -e "  ${GREEN}✓${NC} 通过"
fi
echo ""

# ─── Rule 3: shared（共享内核）不得 import domain / case / infra / adapter ───
echo -e "${YELLOW}[Rule 3]${NC} pkg（共享内核）不得依赖 domain / case / infra / adapter"
checked=$((checked + 1))
before=$violations
check_no_import "internal/pkg" "pkg" "internal/domain"  "internal/domain"
check_no_import "internal/pkg" "pkg" "internal/case"    "internal/case"
check_no_import "internal/pkg" "pkg" "internal/infra"   "internal/infra"
check_no_import "internal/pkg" "pkg" "internal/adapter"  "internal/adapter"
if [ $violations -eq $before ]; then
  echo -e "  ${GREEN}✓${NC} 通过"
fi
echo ""

# ─── Rule 4: infra 不得 import adapter ───
echo -e "${YELLOW}[Rule 4]${NC} infra 不得依赖 adapter"
checked=$((checked + 1))
before=$violations
check_no_import "internal/infra" "infra" "internal/adapter" "internal/adapter"
if [ $violations -eq $before ]; then
  echo -e "  ${GREEN}✓${NC} 通过"
fi
echo ""

# ─── Rule 5: adapter 不得 import infra（adapter 通过接口使用 infra，由 wire 组装）───
echo -e "${YELLOW}[Rule 5]${NC} adapter 不得依赖 infra（通过接口解耦，wire 组装）"
checked=$((checked + 1))
before=$violations
check_no_import "internal/adapter" "adapter" "internal/infra" "internal/infra"
if [ $violations -eq $before ]; then
  echo -e "  ${GREEN}✓${NC} 通过"
fi
echo ""

# ─── Rule 6: control（组合根）不得 import shared 中已迁出的旧路径 ───
echo -e "${YELLOW}[Rule 6]${NC} 已清理的旧路径不应被引用"
checked=$((checked + 1))
before=$violations
stale_paths=(
  "internal/pkg/storage"
  "internal/pkg/notify"
  "internal/pkg/pipeline/builtin"
)
for sp in "${stale_paths[@]}"; do
  while IFS= read -r file; do
    while IFS= read -r line; do
      violations=$((violations + 1))
      echo -e "  ${RED}✗${NC} ${file#"$ROOT/"}  →  imports stale ${YELLOW}${sp}${NC}"
      echo -e "    ${line}"
    done < <(grep -n "\".*/${sp}" "$file" 2>/dev/null || true)
  done < <(find "$ROOT/internal" "$ROOT/cmd" -name '*.go' -not -name '*_test.go' -not -path '*/testdata/*' 2>/dev/null)
done
if [ $violations -eq $before ]; then
  echo -e "  ${GREEN}✓${NC} 通过"
fi
echo ""

# ─── 汇总 ───
echo "═══════════════════════════════════════════════════════"
if [ $violations -eq 0 ]; then
  echo -e " ${GREEN}全部通过${NC}  检查 ${checked} 条规则，0 个违规"
else
  echo -e " ${RED}发现 ${violations} 个违规${NC}  检查 ${checked} 条规则"
fi
echo "═══════════════════════════════════════════════════════"
echo ""

exit $violations
