import os
from enum import IntEnum
from pathlib import Path

from google.protobuf.text_format import Merge, MessageToString

from proto import request_pb2


SUPPORTED_LANGUAGES = [
    "bash",
    "c",
    "cpp",
    "java",
    "javascript",
    "python",
    "python3",
    "verilog",
    "zip",
]
REQUEST_FILE_NAME = "request.txt"
REPLY_FILE_NAME = "reply.txt"
TESTCASES_DIR = Path(__file__).parent / "testcases"


class ValidationResult(IntEnum):
    OK = 0
    ONLY_STATUS_MATCH = 1
    STATUS_MISMATCH = 2


def get_testcase_dirs(language=None):
    if not TESTCASES_DIR.exists():
        return []

    test_filter = os.environ.get("TEST_FILTER", "")
    testcase_dirs = []

    if language and language not in SUPPORTED_LANGUAGES:
        return []

    if language:
        # Get test cases for specific language
        lang_path = TESTCASES_DIR / language
        if lang_path.exists():
            testcase_dirs = [
                str(test_dir)
                for test_dir in lang_path.iterdir()
                if test_dir.is_dir()
                and (test_dir / REQUEST_FILE_NAME).exists()
            ]
    else:
        # Get all test cases from all languages
        testcase_dirs = [
            str(test_dir)
            for lang_dir in TESTCASES_DIR.iterdir()
            if lang_dir.is_dir()
            for test_dir in lang_dir.iterdir()
            if test_dir.is_dir() and (test_dir / REQUEST_FILE_NAME).exists()
        ]

    # Apply filter if set
    if test_filter:
        testcase_dirs = [
            path
            for path in testcase_dirs
            if test_filter in f"{Path(path).parent.name}/{Path(path).name}"
            or test_filter in Path(path).name
        ]

    return sorted(testcase_dirs)


def load_testcase(testcase_dir):
    request_path = Path(testcase_dir) / REQUEST_FILE_NAME
    reply_path = Path(testcase_dir) / REPLY_FILE_NAME

    if not request_path.exists():
        return (None, None)

    request_text = request_path.read_text()
    reply_text = reply_path.read_text() if reply_path.exists() else None

    return (request_text, reply_text)


def load_testcase_protobuf(testcase_dir):
    request_text, reply_text = load_testcase(testcase_dir)

    if not request_text:
        raise FileNotFoundError(f"Request file not found in {testcase_dir}")
    if not reply_text:
        raise FileNotFoundError(f"Reply file not found in {testcase_dir}")

    request = Merge(request_text, request_pb2.CodeRequest())
    reply = Merge(reply_text, request_pb2.CodeReply())

    return request, reply


def create_json_payload(request):
    message = request if isinstance(request, str) else MessageToString(request)
    return {"message": message}


def parse_protobuf_text(text, message_type):
    message = message_type()
    return Merge(text, message)


def clean_response(response):
    cleaned = request_pb2.CodeReply()
    cleaned.CopyFrom(response)

    if hasattr(cleaned, "compilation_result") and cleaned.compilation_result:
        cleaned.compilation_result.nsjail_output = ""

    for result in cleaned.test_case_results:
        result.nsjail_output = ""

    if hasattr(cleaned, "nsjail_output"):
        cleaned.nsjail_output = ""

    return cleaned


def validate_response(actual, expected):
    clean_expected = clean_response(expected)
    clean_actual = clean_response(actual)

    if clean_actual == clean_expected:
        return ValidationResult.OK
    if clean_actual.overall_status == clean_expected.overall_status:
        return ValidationResult.ONLY_STATUS_MATCH
    return ValidationResult.STATUS_MISMATCH


def validate_response_status_only(actual_text, expected_text):
    if not expected_text:
        return True, "No expected reply to validate"

    try:
        actual = parse_protobuf_text(actual_text, request_pb2.CodeReply)
        expected = parse_protobuf_text(expected_text, request_pb2.CodeReply)

        if actual.overall_status == expected.overall_status:
            return True, "Status matches"
        return (
            False,
            f"Status mismatch: got {actual.overall_status}, expected {expected.overall_status}",
        )
    except Exception as e:
        return False, f"Validation error: {e!s}"


def get_test_name(testcase_dir):
    path = Path(testcase_dir)
    return (
        f"{path.parent.name}/{path.name}"
        if path.parent.name in SUPPORTED_LANGUAGES
        else path.name
    )
