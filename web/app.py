#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Flask 后端服务 - 车辆环境健康检查 Web 界面
用法:
    pip install flask flask-cors
    python app.py
    
    访问: http://localhost:5000
"""

import os
import json
import subprocess
import threading
from datetime import datetime
from flask import Flask, jsonify, request, send_from_directory
from flask_cors import CORS

app = Flask(__name__, static_folder='dist', static_url_path='')
CORS(app)

# 配置
CHECK_CMD = os.environ.get('CHECK_CMD', './check_json')
CHECK_TIMEOUT = int(os.environ.get('CHECK_TIMEOUT', 120))

# 缓存最近的检测结果
last_result = None
last_check_time = None
is_checking = False
check_lock = threading.Lock()


def run_check(items=None):
    """运行检测命令并返回JSON结果"""
    global last_result, last_check_time, is_checking
    
    cmd = [CHECK_CMD]
    if items:
        cmd.extend(['-items', items])
    
    try:
        result = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            timeout=CHECK_TIMEOUT
        )
        
        # 解析JSON输出
        output = result.stdout.strip()
        if output:
            data = json.loads(output)
            with check_lock:
                last_result = data
                last_check_time = datetime.now().isoformat()
            return data
        else:
            return {
                'success': False,
                'error': result.stderr or '检测程序无输出',
                'timestamp': datetime.now().isoformat()
            }
    except subprocess.TimeoutExpired:
        return {
            'success': False,
            'error': f'检测超时（>{CHECK_TIMEOUT}秒）',
            'timestamp': datetime.now().isoformat()
        }
    except json.JSONDecodeError as e:
        return {
            'success': False,
            'error': f'JSON解析失败: {str(e)}',
            'raw_output': result.stdout if 'result' in dir() else '',
            'timestamp': datetime.now().isoformat()
        }
    except FileNotFoundError:
        return {
            'success': False,
            'error': f'检测程序不存在: {CHECK_CMD}',
            'timestamp': datetime.now().isoformat()
        }
    except Exception as e:
        return {
            'success': False,
            'error': str(e),
            'timestamp': datetime.now().isoformat()
        }
    finally:
        with check_lock:
            is_checking = False


@app.route('/')
def index():
    """返回前端页面"""
    return send_from_directory(app.static_folder, 'index.html')


@app.route('/api/check', methods=['POST'])
def api_check():
    """执行检测"""
    global is_checking
    
    with check_lock:
        if is_checking:
            return jsonify({
                'success': False,
                'error': '检测正在进行中，请稍后'
            }), 429
        is_checking = True
    
    # 获取要检测的项目
    data = request.get_json() or {}
    items = data.get('items', '')
    
    # 异步执行检测
    result = run_check(items)
    return jsonify(result)


@app.route('/api/check/async', methods=['POST'])
def api_check_async():
    """异步执行检测（立即返回，后台执行）"""
    global is_checking
    
    with check_lock:
        if is_checking:
            return jsonify({
                'success': False,
                'error': '检测正在进行中，请稍后'
            }), 429
        is_checking = True
    
    data = request.get_json() or {}
    items = data.get('items', '')
    
    # 后台线程执行
    thread = threading.Thread(target=run_check, args=(items,))
    thread.daemon = True
    thread.start()
    
    return jsonify({
        'success': True,
        'message': '检测已开始，请通过 /api/status 查询结果'
    })


@app.route('/api/status', methods=['GET'])
def api_status():
    """获取当前状态"""
    with check_lock:
        return jsonify({
            'is_checking': is_checking,
            'last_result': last_result,
            'last_check_time': last_check_time
        })


@app.route('/api/result', methods=['GET'])
def api_result():
    """获取最近的检测结果"""
    with check_lock:
        if last_result:
            return jsonify(last_result)
        else:
            return jsonify({
                'success': False,
                'error': '暂无检测结果，请先执行检测'
            }), 404


@app.route('/api/items', methods=['GET'])
def api_items():
    """获取所有检测项列表"""
    return jsonify({
        'items': [
            {'id': 1, 'name': '车机状态', 'category': 'car'},
            {'id': 2, 'name': 'MDC1A (192.168.30.41) NAS挂载', 'category': 'mount'},
            {'id': 3, 'name': 'MDC2 (192.168.30.143) NAS挂载', 'category': 'mount'},
            {'id': 4, 'name': 'MDC1A 左侧 DTOF', 'category': 'topic'},
            {'id': 5, 'name': 'MDC1A 右侧 DTOF', 'category': 'topic'},
            {'id': 6, 'name': 'MDC1A 后向 DTOF', 'category': 'topic'},
            {'id': 7, 'name': 'MDC1A 感知目标列表', 'category': 'topic'},
            {'id': 8, 'name': 'MDC1A 融合感知目标列表', 'category': 'topic'},
            {'id': 9, 'name': 'MDC1A 前向激光雷达', 'category': 'topic'},
            {'id': 10, 'name': 'MDC2 后向激光雷达', 'category': 'topic'},
            {'id': 11, 'name': 'MDC2 右侧激光雷达', 'category': 'topic'},
            {'id': 12, 'name': 'MDC2 车顶激光雷达', 'category': 'topic'},
            {'id': 13, 'name': 'MDC2 左侧激光雷达', 'category': 'topic'},
        ],
        'categories': [
            {'key': 'car', 'name': '车机状态'},
            {'key': 'mount', 'name': 'NAS挂载'},
            {'key': 'topic', 'name': 'Topic检测'},
        ],
        'shortcuts': [
            {'key': 'all', 'name': '全量检测'},
            {'key': 'car', 'name': '仅车机'},
            {'key': 'mount', 'name': '仅挂载'},
            {'key': 'topic', 'name': '仅Topic'},
            {'key': 'mdc1', 'name': 'MDC1相关'},
            {'key': 'mdc2', 'name': 'MDC2相关'},
        ]
    })


# 错误处理
@app.errorhandler(404)
def not_found(e):
    # 如果是API请求，返回JSON
    if request.path.startswith('/api/'):
        return jsonify({'error': 'Not found'}), 404
    # 否则返回前端页面（SPA路由支持）
    return send_from_directory(app.static_folder, 'index.html')


if __name__ == '__main__':
    import argparse
    parser = argparse.ArgumentParser(description='车辆环境健康检查 Web 服务')
    parser.add_argument('--host', default='0.0.0.0', help='监听地址')
    parser.add_argument('--port', type=int, default=5000, help='监听端口')
    parser.add_argument('--debug', action='store_true', help='调试模式')
    parser.add_argument('--check-cmd', default='./check_json', help='检测程序路径')
    args = parser.parse_args()
    
    CHECK_CMD = args.check_cmd
    
    print(f"启动服务: http://{args.host}:{args.port}")
    print(f"检测程序: {CHECK_CMD}")
    
    app.run(host=args.host, port=args.port, debug=args.debug)
