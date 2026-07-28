import fs from "node:fs";
import path from "node:path";

import type {
  FullConfig,
  FullResult,
  Reporter,
  TestCase,
  TestResult,
} from "@playwright/test/reporter";

type RawAttachment = {
  name: string;
  content_type: string;
  path?: string;
};

type RawTestResult = {
  title: string;
  status: TestResult["status"];
  expected_status: TestCase["expectedStatus"];
  started_at: string;
  duration_ms: number;
  error?: string;
  attachments: RawAttachment[];
};

type RawReport = {
  schema_version: 1;
  started_at: string;
  finished_at: string;
  status: FullResult["status"];
  tests: RawTestResult[];
};

export default class ReleaseReporter implements Reporter {
  private startedAt = new Date();
  private readonly tests: RawTestResult[] = [];

  onBegin(_config: FullConfig): void {
    this.startedAt = new Date();
  }

  onTestEnd(test: TestCase, result: TestResult): void {
    this.tests.push({
      title: test.title,
      status: result.status,
      expected_status: test.expectedStatus,
      started_at: result.startTime.toISOString(),
      duration_ms: result.duration,
      error: result.error?.message,
      attachments: result.attachments.map((attachment) => ({
        name: attachment.name,
        content_type: attachment.contentType,
        path: attachment.path,
      })),
    });
  }

  onEnd(result: FullResult): void {
    const reportPath = requiredAbsoluteReportPath();
    const report: RawReport = {
      schema_version: 1,
      started_at: this.startedAt.toISOString(),
      finished_at: new Date().toISOString(),
      status: result.status,
      tests: this.tests,
    };
    fs.mkdirSync(path.dirname(reportPath), { recursive: true });

    // Rename a complete temporary file so the Go runner never parses a
    // partially written report after Playwright exits.
    const temporaryPath = `${reportPath}.tmp`;
    fs.writeFileSync(temporaryPath, `${JSON.stringify(report, null, 2)}\n`, {
      encoding: "utf8",
      mode: 0o644,
    });
    fs.renameSync(temporaryPath, reportPath);
  }
}

function requiredAbsoluteReportPath(): string {
  const value = process.env.STELLA_E2E_REPORT_PATH;
  if (!value) {
    throw new Error("STELLA_E2E_REPORT_PATH is required");
  }
  if (!path.isAbsolute(value)) {
    throw new Error("STELLA_E2E_REPORT_PATH must be an absolute path");
  }
  return value;
}
