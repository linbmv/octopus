/**
 * API 响应基础类型
 */
export interface ApiResponse<T = unknown> {
    code?: number;
    message?: string;
    data?: T;
}

/** Normalized error thrown by the API client. */
export class ApiError extends Error {
    readonly status: number;
    readonly code: string;
    readonly details?: Record<string, unknown>;

    constructor(status: number, code: string, message: string, details?: Record<string, unknown>) {
        super(message);
        this.name = 'ApiError';
        this.status = status;
        this.code = code;
        this.details = details;
    }
}

/**
 * 分页请求参数
 */
export interface PaginationParams {
    page: number;
    page_size: number;
}

/**
 * 分页响应数据
 */
export interface PaginatedResponse<T> {
    items: T[];
    total: number;
    page: number;
    page_size: number;
    total_pages: number;
}

/**
 * HTTP 状态码常量
 */
export const HttpStatus = {
    OK: 200,
    CREATED: 201,
    NO_CONTENT: 204,
    BAD_REQUEST: 400,
    UNAUTHORIZED: 401,
    FORBIDDEN: 403,
    NOT_FOUND: 404,
    CONFLICT: 409,
    INTERNAL_SERVER_ERROR: 500,
} as const;

export type HttpStatusCode = typeof HttpStatus[keyof typeof HttpStatus];
