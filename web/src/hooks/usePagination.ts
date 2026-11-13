import {useCallback, useId, useState} from "react";
import type {TablePaginationConfig} from "antd";

export const pageSizeOptions = [15, 20, 25, 50, 100];
export const localPagination: TablePaginationConfig = {
  defaultPageSize: 15,
  pageSizeOptions,
  showSizeChanger: true,
};

export function usePagination(initialPage: number | (() => number) = 1) {
  const sizeID = useId();
  const [page, setPage] = useState(initialPage);
  const [pageSize, setPageSize] = useState(15);
  const onChange = useCallback((nextPage: number, nextSize: number) => {
    setPage(nextSize === pageSize ? nextPage : 1);
    setPageSize(nextSize);
  }, [pageSize]);
  const pagination = {
    current: page,
    pageSize,
    pageSizeOptions,
    showSizeChanger: {id: sizeID, "aria-label": "每页记录数"},
    onChange,
  };
  return [page, setPage, pagination] as const;
}
