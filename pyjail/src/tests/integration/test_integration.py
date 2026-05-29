from unittest.mock import MagicMock

import pytest

from nsjps_flask.server import ProgrammingServer
from proto import request_pb2
from tests.utils import (
    ValidationResult,
    create_json_payload,
    get_test_name,
    get_testcase_dirs,
    load_testcase_protobuf,
    parse_protobuf_text,
    validate_response,
)


def execute_request(request):
    server = ProgrammingServer()
    mock_args = MagicMock()
    mock_args.parse_args.return_value = create_json_payload(request)
    server.reqparse = mock_args
    response = server.post()
    return parse_protobuf_text(response["message"], request_pb2.CodeReply)


def run_integration_test(testcase_dir):
    request, expected_reply = load_testcase_protobuf(testcase_dir)
    actual_reply = execute_request(request)
    return validate_response(actual_reply, expected_reply)


@pytest.mark.parametrize("testcase_dir", get_testcase_dirs())
def test_integration_case(testcase_dir):
    result = run_integration_test(testcase_dir)
    testcase_name = get_test_name(testcase_dir)
    assert result == ValidationResult.OK, f"{result.name} - {testcase_name}"
