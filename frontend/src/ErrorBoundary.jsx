// 全局错误边界：渲染/懒加载失败时兜底展示，避免整页白屏。
import { Component } from "react";

export default class ErrorBoundary extends Component {
  constructor(props) {
    super(props);
    this.state = { hasError: false };
  }

  static getDerivedStateFromError() {
    return { hasError: true };
  }

  componentDidCatch(error, info) {
    console.error("[ErrorBoundary]", error, info);
  }

  handleReload = () => {
    window.location.reload();
  };

  render() {
    if (this.state.hasError) {
      return (
        <div className="error-boundary" role="alert">
          <h2 className="error-boundary-title">页面出错了</h2>
          <p className="error-boundary-text">
            加载或渲染过程中发生异常，请刷新页面重试。
          </p>
          <button
            className="btn btn-primary"
            type="button"
            onClick={this.handleReload}
          >
            刷新页面
          </button>
        </div>
      );
    }
    return this.props.children;
  }
}
